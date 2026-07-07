package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/constants"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/api/cloudinit"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/api/qemu"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/api/tasks"
	pve "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
)

var errDirectoryTraversal = errors.New("invalid path: contains directory traversal")

type vmConfig struct {
	host     string
	proto    string
	port     int
	username string
	password string
	apiToken string
	node     string
	srcVMID  int
	vmid     int
	storage  string
	bridge   string
}

func main() {
	config := getVMConfig()
	client := createPVEClient(config)

	qemuAPI := qemu.New(client)
	cloudInitAPI := cloudinit.New(client)
	tasksAPI := tasks.New(client)
	ctx := context.Background()

	cloneVM(ctx, qemuAPI, tasksAPI, config)
	configureVM(ctx, client, qemuAPI, config)
	setupCloudInit(ctx, client, cloudInitAPI, config)
	startVM(ctx, qemuAPI, tasksAPI, config)

	log.Println("Clone + cloud-init + start complete.")
}

func getVMConfig() vmConfig {
	return vmConfig{
		host:     getenv("PVE_HOST", "localhost"),
		proto:    getenv("PVE_PROTO", "https"),
		port:     getenvInt("PVE_PORT", constants.ProxmoxDefaultPort),
		username: getenv("PVE_USERNAME", ""),
		password: getenv("PVE_PASSWORD", ""),
		apiToken: getenv("PVE_API_TOKEN", ""),
		node:     getenv("PVE_NODE", "pve"),
		srcVMID:  getenvInt("PVE_TEMPLATE_VMID", constants.DefaultTemplateVMID),
		vmid:     getenvInt("PVE_VMID", constants.DefaultNewVMID),
		storage:  getenv("PVE_TARGET_STORAGE", "local-lvm"),
		bridge:   getenv("PVE_BRIDGE", "vmbr0"),
	}
}

func createPVEClient(config vmConfig) pve.Client { //nolint:ireturn // Helper function for example
	opts := pve.Options{Host: config.host, Protocol: config.proto, Port: config.port}
	if config.apiToken != "" {
		opts.APIToken = config.apiToken
	} else {
		opts.Username = config.username
		opts.Password = config.password
	}

	client, err := pve.NewClient(opts)
	if err != nil {
		log.Fatalf("client: %v", err)
	}

	return client
}

func cloneVM(ctx context.Context, qemuAPI qemu.Service, tasksAPI tasks.Service, config vmConfig) {
	log.Printf("Cloning %d -> %d on %s...\n", config.srcVMID, config.vmid, config.node)
	params := map[string]interface{}{
		"newid":   config.vmid,
		"full":    1,
		"target":  config.node,
		"storage": config.storage,
	}

	upid, err := qemuAPI.Clone(ctx, config.node, config.srcVMID, params)
	if err != nil {
		log.Fatalf("clone: %v", err)
	}

	_, err = tasksAPI.Wait(ctx, config.node, upid, &tasks.WaitOptions{TimeoutSeconds: constants.LongTaskTimeoutSeconds, IntervalMillis: constants.TaskIntervalMillis})
	if err != nil {
		log.Fatalf("wait clone: %v", err)
	}
}

func configureVM(ctx context.Context, client pve.Client, qemuAPI qemu.Service, config vmConfig) {
	_, err := client.PutCtx(ctx, fmt.Sprintf("/nodes/%s/qemu/%d/config", config.node, config.vmid), map[string]interface{}{
		"net0": "virtio,bridge=" + config.bridge,
	})
	if err != nil {
		log.Fatalf("set net0: %v", err)
	}

	_, err = qemuAPI.AttachDisk(ctx, config.node, config.vmid, fmt.Sprintf("%s:vm-%d-disk-1", config.storage, config.vmid), "scsi", nil)
	if err != nil {
		log.Fatalf("attach disk: %v", err)
	}
}

func setupCloudInit(ctx context.Context, client pve.Client, cloudInitAPI cloudinit.Service, config vmConfig) {
	userData, networkData := readCloudInitFiles()

	err := cloudInitAPI.AttachWithNetwork(ctx, config.node, config.vmid, config.storage, userData, networkData)
	if err != nil {
		log.Fatalf("attach cloudinit: %v", err)
	}

	ipcfg, _ := cloudInitAPI.BuildIPConfigs([]cloudinit.NICSpec{{DHCP: true}}, nil)

	_, err = client.PutCtx(ctx, fmt.Sprintf("/nodes/%s/qemu/%d/config", config.node, config.vmid), anyMap(ipcfg))
	if err != nil {
		log.Fatalf("set ipcfg: %v", err)
	}
}

func readCloudInitFiles() ([]byte, []byte) {
	var userData, networkData []byte

	if path := os.Getenv("PVE_CI_USERDATA_FILE"); path != "" {
		b, err := secureReadFile(path)
		if err == nil {
			userData = b
		}
	}

	if path := os.Getenv("PVE_CI_NETWORKDATA_FILE"); path != "" {
		b, err := secureReadFile(path)
		if err == nil {
			networkData = b
		}
	}

	return userData, networkData
}

func startVM(ctx context.Context, qemuAPI qemu.Service, tasksAPI tasks.Service, config vmConfig) {
	log.Printf("Starting %d...\n", config.vmid)

	upid, err := qemuAPI.Start(ctx, config.node, config.vmid)
	if err != nil {
		log.Fatalf("start: %v", err)
	}

	_, err = tasksAPI.Wait(ctx, config.node, upid, &tasks.WaitOptions{TimeoutSeconds: constants.LongTaskTimeoutSeconds, IntervalMillis: constants.TaskIntervalMillis})
	if err != nil {
		log.Fatalf("wait start: %v", err)
	}
}

func getenv(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}

	return d
}
func getenvInt(k string, defaultValue int) int {
	if v := os.Getenv(k); v != "" {
		i, err := strconv.Atoi(v)
		if err == nil {
			return i
		}
	}

	return defaultValue
}
func anyMap(m map[string]string) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = v
	}

	return out
}

// secureReadFile reads a file with path validation to prevent directory traversal attacks.
func secureReadFile(path string) ([]byte, error) {
	// Clean the path and resolve any symbolic links
	cleanPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, fmt.Errorf("failed to evaluate symlinks for path %q: %w", path, err)
	}

	// Ensure the path doesn't contain directory traversal patterns
	if strings.Contains(cleanPath, "..") {
		return nil, errDirectoryTraversal
	}

	data, err := os.ReadFile(cleanPath) // #nosec G304 - path is validated
	if err != nil {
		return nil, fmt.Errorf("failed to read file %q: %w", cleanPath, err)
	}

	return data, nil
}
