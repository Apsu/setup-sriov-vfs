package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const infinibandBasePath = "/sys/class/infiniband"

func main() {
	// Read configuration from environment variables.
	numVFsStr := os.Getenv("NUM_VFS")
	if numVFsStr == "" {
		fmt.Fprintln(os.Stderr, "NUM_VFS environment variable is not set.")
		os.Exit(1)
	}
	numVFs, err := strconv.Atoi(numVFsStr)
	if err != nil || numVFs <= 0 {
		fmt.Fprintf(os.Stderr, "Invalid NUM_VFS value: %s\n", numVFsStr)
		os.Exit(1)
	}

	// Optional: if set, only configure HCAs whose device ID matches DEVICE_ID.
	deviceID := os.Getenv("DEVICE_ID")

	// Get a machine-unique prefix from /etc/machine-id.
	machinePrefix, err := getMachinePrefix()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting machine prefix: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Machine prefix for VF MACs: %s\n", machinePrefix)

	// Global VF counter for the entire machine.
	var vfCounter uint64 = 0

	// Retrieve all HCA entries (they are symlinks) in infinibandBasePath.
	hcas, err := getHCAs(infinibandBasePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading HCAs: %v\n", err)
		os.Exit(1)
	}
	if len(hcas) == 0 {
		fmt.Fprintf(os.Stderr, "No HCAs found in %s\n", infinibandBasePath)
		os.Exit(1)
	}

	// Process each HCA.
	for _, hca := range hcas {
		if deviceID != "" {
			ok, err := checkDeviceId(hca, deviceID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error checking device ID for %s: %v\n", hca, err)
				continue
			}
			if !ok {
				fmt.Printf("Skipping HCA %s (device ID mismatch)\n", hca)
				continue
			}
		}

		fmt.Printf("Configuring HCA: %s\n", hca)
		if err := configureHCA(hca, numVFs, machinePrefix, &vfCounter); err != nil {
			fmt.Fprintf(os.Stderr, "Error configuring HCA %s: %v\n", hca, err)
			continue
		}
	}

	fmt.Println("SR-IOV VF configuration completed for all matching HCAs.")
}

// getMachinePrefix reads /etc/machine-id, takes the first 8 hex digits,
// and formats them as "xx:xx:xx:xx".
func getMachinePrefix() (string, error) {
	data, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(data))
	if len(id) < 8 {
		return "", fmt.Errorf("machine-id too short: %s", id)
	}
	prefixRaw := id[:8] // first 8 hex digits
	var parts []string
	for i := 0; i < 8; i += 2 {
		parts = append(parts, prefixRaw[i:i+2])
	}
	return strings.Join(parts, ":"), nil
}

// getHCAs returns the names of all entries in infinibandBasePath whose symlink targets are directories.
func getHCAs(basePath string) ([]string, error) {
	entries, err := os.ReadDir(basePath)
	if err != nil {
		return nil, err
	}
	var hcas []string
	for _, entry := range entries {
		fullPath := filepath.Join(basePath, entry.Name())
		info, err := os.Stat(fullPath) // follows symlinks
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not stat %s: %v\n", fullPath, err)
			continue
		}
		if info.IsDir() {
			hcas = append(hcas, entry.Name())
		}
	}
	return hcas, nil
}

// checkDeviceId reads /sys/class/infiniband/<hca>/device/device and compares it with expectedDeviceID.
func checkDeviceId(hca, expectedDeviceID string) (bool, error) {
	devicePath := filepath.Join(infinibandBasePath, hca, "device", "device")
	data, err := os.ReadFile(devicePath)
	if err != nil {
		return false, err
	}
	id := strings.TrimSpace(string(data))
	return id == expectedDeviceID, nil
}

// configureHCA resets the VF count, assigns new VF MACs, and then unbinds/rebinds VF devices.
func configureHCA(hca string, numVFs int, machinePrefix string, vfCounter *uint64) error {
	// Reset the VF count.
	if err := setSriovNumVFs(hca, 0); err != nil {
		return fmt.Errorf("failed to reset sriov_numvfs: %v", err)
	}
	if err := setSriovNumVFs(hca, numVFs); err != nil {
		return fmt.Errorf("failed to set sriov_numvfs to %d: %v", numVFs, err)
	}

	// Assign VF MACs using the machine prefix and a global VF counter.
	if err := assignVFMacs(hca, numVFs, machinePrefix, vfCounter); err != nil {
		return fmt.Errorf("failed to assign VF MACs: %v", err)
	}

	// Unbind and rebind the VF devices to force node_guid reinitialization.
	if err := rebindVFDevices(hca); err != nil {
		return fmt.Errorf("failed to rebind VF devices: %v", err)
	}
	return nil
}

// setSriovNumVFs writes the desired VF count to /sys/class/infiniband/<hca>/device/sriov_numvfs.
func setSriovNumVFs(hca string, num int) error {
	sriovPath := filepath.Join(infinibandBasePath, hca, "device", "sriov_numvfs")
	return os.WriteFile(sriovPath, []byte(strconv.Itoa(num)), 0644)
}

// assignVFMacs generates and writes a VF MAC for each VF in the given HCA using the machine prefix and a global counter.
// The VF MAC is formatted as "02:<machinePrefix>:<VF_counter>" (e.g. "02:79:a0:e6:4b:00").
func assignVFMacs(hca string, numVFs int, machinePrefix string, vfCounter *uint64) error {
	for i := 0; i < numVFs; i++ {
		vfIndex := *vfCounter
		if vfIndex > 0xff {
			return fmt.Errorf("global VF index %d exceeds 255", vfIndex)
		}
		vfMAC := fmt.Sprintf("02:%s:%02x", machinePrefix, vfIndex)
		vfMacPath := filepath.Join(infinibandBasePath, hca, "device", "sriov", strconv.Itoa(i), "mac")
		if err := os.WriteFile(vfMacPath, []byte(vfMAC), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to write VF %d MAC to %s: %v\n", i, vfMacPath, err)
		} else {
			fmt.Printf("HCA %s: Assigned VF %d MAC: %s\n", hca, i, vfMAC)
		}
		*vfCounter++
	}
	return nil
}

// rebindVFDevices unbinds and rebinds each VF (found as "virtfn*" entries under /sys/class/infiniband/<hca>/device)
// so that the driver reinitializes the VF (and thus recalculates the node_guid based on the new MAC).
func rebindVFDevices(hca string) error {
	pfDeviceDir := filepath.Join(infinibandBasePath, hca, "device")
	entries, err := os.ReadDir(pfDeviceDir)
	if err != nil {
		return fmt.Errorf("error reading PF device directory %s: %v", pfDeviceDir, err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "virtfn") {
			continue
		}
		virtfnPath := filepath.Join(pfDeviceDir, entry.Name())
		target, err := os.Readlink(virtfnPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not read symlink %s: %v\n", virtfnPath, err)
			continue
		}
		absTarget, err := filepath.Abs(filepath.Join(pfDeviceDir, target))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not resolve absolute path for %s: %v\n", virtfnPath, err)
			continue
		}
		pciAddr := filepath.Base(absTarget)
		fmt.Printf("Rebinding VF with PCI address: %s\n", pciAddr)

		vfDriverPath := filepath.Join("/sys/bus/pci/devices", pciAddr, "driver")
		driverLink, err := os.Readlink(vfDriverPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not read driver symlink for VF %s: %v\n", pciAddr, err)
			continue
		}
		driverName := filepath.Base(driverLink)

		unbindPath := filepath.Join("/sys/bus/pci/drivers", driverName, "unbind")
		if err := os.WriteFile(unbindPath, []byte(pciAddr), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to unbind VF %s: %v\n", pciAddr, err)
		} else {
			fmt.Printf("Unbound VF %s from driver %s\n", pciAddr, driverName)
		}

		bindPath := filepath.Join("/sys/bus/pci/drivers", driverName, "bind")
		if err := os.WriteFile(bindPath, []byte(pciAddr), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to bind VF %s: %v\n", pciAddr, err)
		} else {
			fmt.Printf("Bound VF %s to driver %s\n", pciAddr, driverName)
		}
	}
	return nil
}
