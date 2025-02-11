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

	// Retrieve all HCA entries from infinibandBasePath.
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
		if err := configureHCA(hca, numVFs); err != nil {
			fmt.Fprintf(os.Stderr, "Error configuring HCA %s: %v\n", hca, err)
			continue
		}
	}

	fmt.Println("SR-IOV VF configuration completed for all matching HCAs.")
}

// getHCAs lists all entries in infinibandBasePath and, for each,
// uses os.Stat to follow symlinks and verify the target is a directory.
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

// checkDeviceId reads the device ID for the given HCA and compares it to expectedDeviceID.
func checkDeviceId(hca, expectedDeviceID string) (bool, error) {
	devicePath := filepath.Join(infinibandBasePath, hca, "device", "device")
	data, err := os.ReadFile(devicePath)
	if err != nil {
		return false, err
	}
	id := strings.TrimSpace(string(data))
	return id == expectedDeviceID, nil
}

// configureHCA performs the VF configuration for a single HCA.
func configureHCA(hca string, numVFs int) error {
	// Get the PF MAC address from the first network interface.
	pfMAC, err := getPFMac(hca)
	if err != nil {
		return fmt.Errorf("failed to get PF MAC: %v", err)
	}
	fmt.Printf("HCA %s PF MAC: %s\n", hca, pfMAC)

	// Reset VF count by writing 0, then set the desired number.
	if err := setSriovNumVFs(hca, 0); err != nil {
		return fmt.Errorf("failed to reset sriov_numvfs: %v", err)
	}
	if err := setSriovNumVFs(hca, numVFs); err != nil {
		return fmt.Errorf("failed to set sriov_numvfs to %d: %v", numVFs, err)
	}

	// Assign MAC addresses to each VF.
	if err := assignVFMacs(hca, pfMAC, numVFs); err != nil {
		return fmt.Errorf("failed to assign VF MACs: %v", err)
	}

	// Unbind and rebind VF devices so that the node_guid is reinitialized.
	if err := rebindVFDevices(hca); err != nil {
		return fmt.Errorf("failed to rebind VF devices: %v", err)
	}

	return nil
}

// getPFMac locates the first network interface under
// /sys/class/infiniband/<hca>/device/net/ and returns its MAC address.
func getPFMac(hca string) (string, error) {
	netDir := filepath.Join(infinibandBasePath, hca, "device", "net")
	entries, err := os.ReadDir(netDir)
	if err != nil {
		return "", fmt.Errorf("error reading net directory %s: %v", netDir, err)
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("no network interfaces found in %s", netDir)
	}
	// Use the first interface found.
	iface := entries[0].Name()
	addrPath := filepath.Join(netDir, iface, "address")
	data, err := os.ReadFile(addrPath)
	if err != nil {
		return "", fmt.Errorf("error reading MAC address from %s: %v", addrPath, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// setSriovNumVFs writes the desired VF count to the sriov_numvfs file.
func setSriovNumVFs(hca string, num int) error {
	sriovPath := filepath.Join(infinibandBasePath, hca, "device", "sriov_numvfs")
	return os.WriteFile(sriovPath, []byte(strconv.Itoa(num)), 0644)
}

// assignVFMacs derives and assigns a MAC address to each VF based on the PF MAC.
// The new MAC is built by replacing the first octet with "02" (locally administered)
// and offsetting the last octet by the VF index.
func assignVFMacs(hca, pfMAC string, numVFs int) error {
	octets := strings.Split(pfMAC, ":")
	if len(octets) != 6 {
		return fmt.Errorf("invalid PF MAC address format: %s", pfMAC)
	}
	newFirstOctet := "02"

	// Parse the PF's last octet.
	pfLastOctet, err := strconv.ParseInt(octets[5], 16, 64)
	if err != nil {
		return fmt.Errorf("invalid last octet in PF MAC %s: %v", octets[5], err)
	}

	for i := 0; i < numVFs; i++ {
		newLastOctetVal := (pfLastOctet + int64(i)) % 256
		newLastOctet := fmt.Sprintf("%02x", newLastOctetVal)
		vfMAC := fmt.Sprintf("%s:%s:%s:%s:%s:%s", newFirstOctet, octets[1], octets[2], octets[3], octets[4], newLastOctet)

		vfMacPath := filepath.Join(infinibandBasePath, hca, "device", "sriov", strconv.Itoa(i), "mac")
		if err := os.WriteFile(vfMacPath, []byte(vfMAC), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to write VF %d MAC to %s: %v\n", i, vfMacPath, err)
			continue
		}
		fmt.Printf("HCA %s: Assigned VF %d MAC: %s\n", hca, i, vfMAC)
	}
	return nil
}

// rebindVFDevices unbinds and then rebinds each VF device associated with the given HCA.
// It finds VF PCI devices by reading symlinks named "virtfn*" under the PF's PCI directory.
func rebindVFDevices(hca string) error {
	// The PF's PCI directory is at /sys/class/infiniband/<hca>/device.
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
		// Resolve the symlink to get the VF's PCI device directory.
		target, err := os.Readlink(virtfnPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not read symlink %s: %v\n", virtfnPath, err)
			continue
		}
		// The target is typically a relative path; resolve it to an absolute path.
		absTarget, err := filepath.Abs(filepath.Join(pfDeviceDir, target))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not resolve absolute path for %s: %v\n", virtfnPath, err)
			continue
		}
		// The PCI address is the base name of the VF device directory.
		pciAddr := filepath.Base(absTarget)
		fmt.Printf("Rebinding VF with PCI address: %s\n", pciAddr)

		// Determine the driver by reading the "driver" symlink in the VF's PCI device directory.
		vfDriverPath := filepath.Join("/sys/bus/pci/devices", pciAddr, "driver")
		driverLink, err := os.Readlink(vfDriverPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not read driver symlink for VF %s: %v\n", pciAddr, err)
			continue
		}
		driverName := filepath.Base(driverLink)

		// Unbind the VF.
		unbindPath := filepath.Join("/sys/bus/pci/drivers", driverName, "unbind")
		if err := os.WriteFile(unbindPath, []byte(pciAddr), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to unbind VF %s: %v\n", pciAddr, err)
		} else {
			fmt.Printf("Unbound VF %s from driver %s\n", pciAddr, driverName)
		}

		// Bind the VF.
		bindPath := filepath.Join("/sys/bus/pci/drivers", driverName, "bind")
		if err := os.WriteFile(bindPath, []byte(pciAddr), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to bind VF %s: %v\n", pciAddr, err)
		} else {
			fmt.Printf("Bound VF %s to driver %s\n", pciAddr, driverName)
		}
	}
	return nil
}
