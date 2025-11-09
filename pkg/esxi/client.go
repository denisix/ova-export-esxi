package esxi

import (
	"context"
	"fmt"
	"net/url"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/soap"
)

type Client struct {
	vmomiClient *govmomi.Client
	finder      *find.Finder
	ctx         context.Context
	host        string
	username    string
	password    string
	insecure    bool
}

type Config struct {
	Host     string
	Username string
	Password string
	Insecure bool
}

func NewClient(config Config) *Client {
	return &Client{
		ctx:      context.Background(),
		host:     config.Host,
		username: config.Username,
		password: config.Password,
		insecure: config.Insecure,
	}
}

func (c *Client) Connect() error {
	// Parse the URL
	u, err := soap.ParseURL(c.host)
	if err != nil {
		return fmt.Errorf("failed to parse ESXi URL: %w", err)
	}

	// Set credentials
	u.User = url.UserPassword(c.username, c.password)

	// Create vSphere client
	client, err := govmomi.NewClient(c.ctx, u, c.insecure)
	if err != nil {
		return fmt.Errorf("failed to connect to ESXi: %w", err)
	}

	c.vmomiClient = client
	c.finder = find.NewFinder(client.Client, true)

	// Set datacenter (for ESXi standalone, this is usually "ha-datacenter")
	dc, err := c.finder.DefaultDatacenter(c.ctx)
	if err != nil {
		return fmt.Errorf("failed to find datacenter: %w", err)
	}
	c.finder.SetDatacenter(dc)

	return nil
}

func (c *Client) Disconnect() error {
	if c.vmomiClient != nil {
		return c.vmomiClient.Logout(c.ctx)
	}
	return nil
}

func (c *Client) IsConnected() bool {
	if c.vmomiClient == nil {
		return false
	}

	// Test connection by trying to get session info
	_, err := c.vmomiClient.SessionManager.UserSession(c.ctx)
	return err == nil
}

func (c *Client) GetDatastores() ([]*object.Datastore, error) {
	if c.vmomiClient == nil {
		return nil, fmt.Errorf("not connected to ESXi")
	}

	datastores, err := c.finder.DatastoreList(c.ctx, "*")
	if err != nil {
		return nil, fmt.Errorf("failed to list datastores: %w", err)
	}

	return datastores, nil
}

func (c *Client) GetDatastore(name string) (*object.Datastore, error) {
	if c.vmomiClient == nil {
		return nil, fmt.Errorf("not connected to ESXi")
	}

	datastore, err := c.finder.Datastore(c.ctx, name)
	if err != nil {
		return nil, fmt.Errorf("failed to find datastore %s: %w", name, err)
	}

	return datastore, nil
}

func (c *Client) GetResourcePools() ([]*object.ResourcePool, error) {
	if c.vmomiClient == nil {
		return nil, fmt.Errorf("not connected to ESXi")
	}

	pools, err := c.finder.ResourcePoolList(c.ctx, "*")
	if err != nil {
		return nil, fmt.Errorf("failed to list resource pools: %w", err)
	}

	return pools, nil
}

func (c *Client) GetNetworks() ([]object.NetworkReference, error) {
	if c.vmomiClient == nil {
		return nil, fmt.Errorf("not connected to ESXi")
	}

	networks, err := c.finder.NetworkList(c.ctx, "*")
	if err != nil {
		return nil, fmt.Errorf("failed to list networks: %w", err)
	}

	return networks, nil
}

func (c *Client) GetHostSystem() (*object.HostSystem, error) {
	if c.vmomiClient == nil {
		return nil, fmt.Errorf("not connected to ESXi")
	}

	host, err := c.finder.DefaultHostSystem(c.ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to find host system: %w", err)
	}

	return host, nil
}

// GetSOAPClient returns the underlying SOAP client for direct API calls
func (c *Client) GetSOAPClient() *soap.Client {
	if c.vmomiClient == nil {
		return nil
	}
	return c.vmomiClient.Client.Client
}

// GetVimClient returns the VIM25 client for advanced operations
func (c *Client) GetVimClient() *vim25.Client {
	if c.vmomiClient == nil {
		return nil
	}
	return c.vmomiClient.Client
}

// GetContext returns the context used by the client
func (c *Client) GetContext() context.Context {
	return c.ctx
}

// TestConnection validates the connection and credentials
func (c *Client) TestConnection() error {
	if err := c.Connect(); err != nil {
		return err
	}
	defer c.Disconnect()

	// Try to get server info as a simple test
	about := c.vmomiClient.ServiceContent.About
	if about.Name == "" {
		return fmt.Errorf("failed to get server info")
	}

	return nil
}

// GetServerInfo returns basic information about the ESXi server
func (c *Client) GetServerInfo() (map[string]string, error) {
	if c.vmomiClient == nil {
		return nil, fmt.Errorf("not connected to ESXi")
	}

	about := c.vmomiClient.ServiceContent.About

	info := map[string]string{
		"name":                  about.Name,
		"fullName":              about.FullName,
		"vendor":                about.Vendor,
		"version":               about.Version,
		"build":                 about.Build,
		"osType":                about.OsType,
		"productLine":           about.ProductLineId,
		"apiType":               about.ApiType,
		"apiVersion":            about.ApiVersion,
		"instanceUuid":          about.InstanceUuid,
		"licenseProductName":    about.LicenseProductName,
		"licenseProductVersion": about.LicenseProductVersion,
	}

	return info, nil
}
