package esxi

import (
	"fmt"
	"strings"

	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/ovf"
	"github.com/vmware/govmomi/vim25/types"
)

// ImportVMFromOVF creates a VM from an OVF descriptor after VMDKs have been uploaded
func (c *Client) ImportVMFromOVF(ovfContent string, vmName string, datastoreName string, networkName string) error {
	if c.vmomiClient == nil {
		return fmt.Errorf("not connected to ESXi")
	}

	ctx := c.ctx

	// Parse OVF envelope
	envelope, err := ovf.Unmarshal(strings.NewReader(ovfContent))
	if err != nil {
		return fmt.Errorf("failed to parse OVF: %w", err)
	}

	// Get required ESXi objects
	datastore, err := c.GetDatastore(datastoreName)
	if err != nil {
		return fmt.Errorf("failed to get datastore: %w", err)
	}

	resourcePool, err := c.getDefaultResourcePool()
	if err != nil {
		return fmt.Errorf("failed to get resource pool: %w", err)
	}

	hostSystem, err := c.GetHostSystem()
	if err != nil {
		return fmt.Errorf("failed to get host system: %w", err)
	}

	// Get VM folder
	folder, err := c.getVMFolder()
	if err != nil {
		return fmt.Errorf("failed to get VM folder: %w", err)
	}

	// Create OVF manager
	ovfManager := ovf.NewManager(c.GetVimClient())

	// Build network mappings
	var networkMappings []types.OvfNetworkMapping
	if envelope.Network != nil {
		for _, net := range envelope.Network.Networks {
			networkMappings = append(networkMappings, types.OvfNetworkMapping{
				Name:    net.Name,
				Network: types.ManagedObjectReference{}, // Will be filled by CreateImportSpec
			})
		}
	}

	// Get network reference if specified
	var networkRef types.ManagedObjectReference
	if networkName != "" {
		network, err := c.finder.Network(ctx, networkName)
		if err != nil {
			return fmt.Errorf("failed to find network %s: %w", networkName, err)
		}
		networkRef = network.Reference()

		// Update network mappings with actual network
		for i := range networkMappings {
			networkMappings[i].Network = networkRef
		}
	}

	// Create import spec params
	cisp := types.OvfCreateImportSpecParams{
		EntityName:     vmName,
		NetworkMapping: networkMappings,
		PropertyMapping: []types.KeyValue{},
	}

	// Create import spec
	importSpec, err := ovfManager.CreateImportSpec(ctx, string(ovfContent), resourcePool, datastore, cisp)
	if err != nil {
		return fmt.Errorf("failed to create import spec: %w", err)
	}

	if importSpec.Error != nil && len(importSpec.Error) > 0 {
		return fmt.Errorf("import spec errors: %v", importSpec.Error)
	}

	if importSpec.Warning != nil && len(importSpec.Warning) > 0 {
		// Log warnings but continue
		for _, w := range importSpec.Warning {
			fmt.Printf("Warning: %s\n", w.LocalizedMessage)
		}
	}

	// The import spec contains the VM config spec, but we need to adjust disk file paths
	// since we've already uploaded the VMDKs to {vmName}/ directory
	if importSpec.ImportSpec != nil {
		if configSpec, ok := importSpec.ImportSpec.(*types.VirtualMachineImportSpec); ok {
			// Update disk file paths to point to uploaded VMDKs and ensure we use existing files
			if configSpec.ConfigSpec.DeviceChange != nil {
				for i, change := range configSpec.ConfigSpec.DeviceChange {
					if diskChange, ok := change.(*types.VirtualDeviceConfigSpec); ok {
						if disk, ok := diskChange.Device.(*types.VirtualDisk); ok {
							if backing, ok := disk.Backing.(*types.VirtualDiskFlatVer2BackingInfo); ok {
								// Update the datastore path to match uploaded location
								// Format: [datastoreName] vmName/diskfile.vmdk
								fileName := backing.FileName
								if fileName != "" {
									// Extract just the filename from the path
									var diskFileName string
									// The fileName might be in format like "disk1.vmdk" or have path
									// We need just the base name
									if len(fileName) > 0 {
										// Simple extraction - take everything after last /
										parts := []rune(fileName)
										lastSlash := -1
										for j := len(parts) - 1; j >= 0; j-- {
											if parts[j] == '/' {
												lastSlash = j
												break
											}
										}
										if lastSlash >= 0 {
											diskFileName = string(parts[lastSlash+1:])
										} else {
											diskFileName = fileName
										}
									}

									if diskFileName != "" {
										// Set the path to where we uploaded the VMDK
										newPath := fmt.Sprintf("[%s] %s/%s", datastoreName, vmName, diskFileName)
										backing.FileName = newPath

										// CRITICAL: Clear FileOperation to use existing file instead of creating new one
										// When FileOperation is set to "create", ESXi tries to create a new disk
										// We want to use the existing uploaded VMDK, so we clear this field
										diskChange.FileOperation = ""

										configSpec.ConfigSpec.DeviceChange[i] = diskChange
									}
								}
							}
						}
					}
				}
			}

			// Create the VM using the config spec
			// Since we already uploaded the VMDKs, we create the VM directly
			task, err := folder.CreateVM(ctx, configSpec.ConfigSpec, resourcePool, hostSystem)
			if err != nil {
				return fmt.Errorf("failed to create VM: %w", err)
			}

			// Wait for the VM creation task to complete
			info, err := task.WaitForResult(ctx, nil)
			if err != nil {
				return fmt.Errorf("VM creation task failed: %w", err)
			}

			// Get the created VM reference
			var vmRef types.ManagedObjectReference
			if info != nil && info.Result != nil {
				vmRef = info.Result.(types.ManagedObjectReference)
				fmt.Printf("VM created successfully with reference: %v\n", vmRef)
			} else {
				return fmt.Errorf("failed to get VM reference from creation result")
			}

			// Get the VM object to configure boot order
			vm := object.NewVirtualMachine(c.GetVimClient(), vmRef)

			// Configure boot order to prioritize disk boot
			// This ensures the VM tries to boot from the disk first before network
			bootOptions := &types.VirtualMachineBootOptions{
				BootOrder: []types.BaseVirtualMachineBootOptionsBootableDevice{
					// Boot from disk first
					&types.VirtualMachineBootOptionsBootableDiskDevice{},
					// Then try network boot if disk fails
					&types.VirtualMachineBootOptionsBootableEthernetDevice{},
				},
			}

			// Reconfigure VM to set boot order
			reconfigSpec := types.VirtualMachineConfigSpec{
				BootOptions: bootOptions,
			}

			reconfigTask, err := vm.Reconfigure(ctx, reconfigSpec)
			if err != nil {
				fmt.Printf("Warning: Failed to set boot order: %v\n", err)
				// Don't fail the entire operation, boot order is a nice-to-have
			} else {
				err = reconfigTask.Wait(ctx)
				if err != nil {
					fmt.Printf("Warning: Boot order configuration failed: %v\n", err)
				} else {
					fmt.Printf("Boot order configured: Disk -> Network\n")
				}
			}

			return nil
		}
	}

	return fmt.Errorf("unexpected import spec type")
}

// getDefaultResourcePool gets the default resource pool for the ESXi host
func (c *Client) getDefaultResourcePool() (*object.ResourcePool, error) {
	pools, err := c.GetResourcePools()
	if err != nil {
		return nil, err
	}

	if len(pools) == 0 {
		return nil, fmt.Errorf("no resource pools found")
	}

	// Return the first (default) resource pool
	return pools[0], nil
}

// getVMFolder gets the VM folder for the datacenter
func (c *Client) getVMFolder() (*object.Folder, error) {
	dc, err := c.finder.DefaultDatacenter(c.ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to find datacenter: %w", err)
	}

	folders, err := dc.Folders(c.ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get folders: %w", err)
	}

	return folders.VmFolder, nil
}
