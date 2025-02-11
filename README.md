# setup-sriov-vfs

`setup-sriov-vfs` is a Go-based tool for configuring SR-IOV Virtual Functions (VFs) on InfiniBand/RDMA devices. It generates unique VF MAC addresses (without relying on the PF MAC), resets the VF count, assigns these MACs, and then unbinds/rebinds the VF devices so that the RDMA driver reinitializes the VF and updates its node GUID accordingly.

This tool is especially useful when you want to ensure that VF MAC addresses are globally unique even on systems with similar PF MAC addresses (for example, when using dual-port cards or devices from the same lot). It leverages the unique `/etc/machine-id` on each node to generate a machine-specific MAC prefix and uses a global VF counter to complete the address.

## Features

- **Unique VF MACs:**
  Generates MAC addresses in the format:
  ```
  02:<machinePrefix>:<VF_counter>
  ```
  where:
  - `02` is a constant indicating a locally administered address.
  - `<machinePrefix>` is derived from the first 8 hex digits of `/etc/machine-id` (formatted as four octets).
  - `<VF_counter>` is a global counter across all HCAs on the node (as a two-digit hex number).

- **Device Filtering:**
  Optionally filter which Host Channel Adapters (HCAs) to configure based on a provided `DEVICE_ID`.

- **VF Enable/Disable:**
  Simply setting `NUM_VFS=0` disables all VFs.

- **Driver Rebinding:**
  After writing the VF MACs, the tool unbinds and rebinds each VF so that the RDMA driver recalculates the node GUIDs from the new MAC addresses.

- **PF-only Processing:**
  Only processes physical functions (PFs) in `/sys/class/infiniband` by skipping entries that represent VFs.

## Prerequisites

- Linux with SR-IOV–capable RDMA devices (e.g., Mellanox ConnectX series)
- Go (version 1.16+ recommended)
- Root privileges (the tool writes to sysfs and PCI driver files)

## Configuration

The tool is configured via environment variables (typically loaded via a systemd EnvironmentFile):

- **`NUM_VFS`** (required):
  The number of Virtual Functions (VFs) to create on each HCA.
  Set to `0` to disable all VFs.

- **`DEVICE_ID`** (optional):
  If specified, only HCAs whose device ID (read from `/sys/class/infiniband/<hca>/device/device`) matches this value will be configured.

For example, create `/etc/default/sriov-vfs`:

```bash
# /etc/default/sriov-vfs
NUM_VFS=2
# DEVICE_ID=0x1234  # Uncomment and set to filter HCAs by device ID.
```

## Building the Tool

1. **Clone the repository or copy the source code.**

2. **Build the binary:**

   ```bash
   go build -o setup-sriov-vfs main.go
   ```

3. **(Optional) Move the binary to a system path:**

   ```bash
   sudo mv setup-sriov-vfs /usr/local/bin/
   sudo chmod +x /usr/local/bin/setup-sriov-vfs
   ```

## Usage

`setup-sriov-vfs` is intended to be used as a one-shot tool, commonly run via systemd. When executed, it:

1. Scans `/sys/class/infiniband` for physical HCAs (ignoring VF entries).
2. (Optionally) Filters HCAs by `DEVICE_ID`.
3. Resets the VF count (writes `0` to `sriov_numvfs`).
4. Sets the VF count to the number specified by `NUM_VFS`.
5. Generates unique VF MAC addresses using a machine-specific prefix (from `/etc/machine-id`) plus a global counter.
6. Writes the VF MAC addresses.
7. Unbinds and rebinds each VF so that the RDMA driver reinitializes the VF and recalculates its node GUID.

## Running as a Systemd Service

Create a systemd service unit (e.g., `/etc/systemd/system/setup-sriov-vfs.service`):

```ini
[Unit]
Description=Configure SR-IOV VFs on all matching HCAs
After=network.target

[Service]
Type=oneshot
EnvironmentFile=/etc/default/sriov-vfs
ExecStart=/usr/local/bin/setup-sriov-vfs
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
```

Then reload systemd and enable/start the service:

```bash
sudo systemctl daemon-reload
sudo systemctl enable setup-sriov-vfs.service
sudo systemctl start setup-sriov-vfs.service
```

## Disabling VFs

To disable all VFs, simply set:

```bash
NUM_VFS=0
```

in your `/etc/default/sriov-vfs` and restart the service:

```bash
sudo systemctl restart setup-sriov-vfs.service
```

## Troubleshooting

- **Permissions:**
  Ensure the tool is run as root, as it requires write access to sysfs and PCI driver directories.

- **Device Filtering:**
  If unexpected HCAs are configured, check the value of `DEVICE_ID` and verify the contents of `/sys/class/infiniband/<hca>/device/device`.

- **VF Rebinding:**
  Verify that VF devices are being correctly unbound and rebound by checking the system logs or the service output.

## License

This tool is provided under the MIT License. See the [LICENSE](LICENSE) file for more details.
