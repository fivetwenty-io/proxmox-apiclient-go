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

type vmSetup struct {
	host     string
	proto    string
	port     int
	username string
	password string
	apiToken string
	node     string
	vmid     int
	storage  string
}

func main() {
	setup := getVMSetup()
	client := createPVEClient(setup)

	qemuAPI := qemu.New(client)
	cloudInitAPI := cloudinit.New(client)
	tasksAPI := tasks.New(client)
	ctx := context.Background()

	createVM(ctx, qemuAPI, tasksAPI, setup)
	attachCloudInit(ctx, client, cloudInitAPI, setup)
	startVM(ctx, qemuAPI, tasksAPI, setup)
	verifyVM(ctx, qemuAPI, setup)

	log.Println("Example complete.")
}

func getVMSetup() vmSetup {
	return vmSetup{
		host:     getenv("PVE_HOST", "localhost"),
		proto:    getenv("PVE_PROTO", "https"),
		port:     getenvInt("PVE_PORT", constants.ProxmoxDefaultPort),
		username: getenv("PVE_USERNAME", ""),
		password: getenv("PVE_PASSWORD", ""),
		apiToken: getenv("PVE_API_TOKEN", ""),
		node:     getenv("PVE_NODE", "pve"),
		vmid:     getenvInt("PVE_VMID", constants.DefaultVMID),
		storage:  getenv("PVE_CI_STORAGE", "local-lvm"),
	}
}

func createPVEClient(setup vmSetup) pve.Client { //nolint:ireturn // Factory function pattern
	opts := pve.Options{Host: setup.host, Protocol: setup.proto, Port: setup.port}
	if setup.apiToken != "" {
		opts.APIToken = setup.apiToken

		log.Println("Using API token auth")
	} else {
		if setup.username == "" || setup.password == "" {
			log.Fatal("set PVE_USERNAME and PVE_PASSWORD or PVE_API_TOKEN")
		}

		opts.Username = setup.username
		opts.Password = setup.password
		log.Printf("Using username/password auth for %s\n", setup.username)
	}

	client, err := pve.NewClient(opts)
	if err != nil {
		log.Fatalf("client error: %v", err)
	}

	return client
}

func createVM(ctx context.Context, qemuAPI qemu.Service, tasksAPI tasks.Service, setup vmSetup) {
	log.Printf("Creating VM %d on node %s...", setup.vmid, setup.node)
	createParams := map[string]interface{}{
		"vmid":   setup.vmid,
		"name":   fmt.Sprintf("example-%d", setup.vmid),
		"memory": constants.DefaultMemoryMB,
		"cores":  1,
	}

	upid, err := qemuAPI.Create(ctx, setup.node, createParams)
	if err != nil {
		log.Fatalf("create: %v", err)
	}

	log.Printf("Create UPID: %s", upid)

	_, err = tasksAPI.Wait(ctx, setup.node, upid, &tasks.WaitOptions{TimeoutSeconds: constants.MediumTaskTimeoutSeconds, IntervalMillis: constants.TaskIntervalMillis})
	if err != nil {
		log.Fatalf("wait create: %v", err)
	}
}

func attachCloudInit(ctx context.Context, client pve.Client, cloudInitAPI cloudinit.Service, setup vmSetup) {
	log.Printf("Attaching cloud-init on %s...", setup.storage)

	userData, networkData := readCloudInitFiles()

	err := cloudInitAPI.AttachWithNetwork(ctx, setup.node, setup.vmid, setup.storage, userData, networkData)
	if err != nil {
		log.Fatalf("attach cloud-init: %v", err)
	}

	ipcfg, _ := cloudInitAPI.BuildIPConfigs([]cloudinit.NICSpec{{DHCP: true}}, nil)
	if len(ipcfg) > 0 {
		_, err := client.PutCtx(ctx, fmt.Sprintf("/nodes/%s/qemu/%d/config", setup.node, setup.vmid), anyMap(ipcfg))
		if err != nil {
			log.Fatalf("set ipconfig: %v", err)
		}
	}
}

func readCloudInitFiles() ([]byte, []byte) {
	var userData, networkData []byte

	if path := os.Getenv("PVE_CI_USERDATA_FILE"); path != "" {
		b, err := secureReadFile(path)
		if err != nil {
			log.Fatalf("read user-data: %v", err)
		}

		userData = b
	}

	if path := os.Getenv("PVE_CI_NETWORKDATA_FILE"); path != "" {
		b, err := secureReadFile(path)
		if err != nil {
			log.Fatalf("read network-data: %v", err)
		}

		networkData = b
	}

	return userData, networkData
}

func startVM(ctx context.Context, qemuAPI qemu.Service, tasksAPI tasks.Service, setup vmSetup) {
	log.Printf("Starting VM %d...", setup.vmid)

	upid, err := qemuAPI.Start(ctx, setup.node, setup.vmid)
	if err != nil {
		log.Fatalf("start: %v", err)
	}

	log.Printf("Start UPID: %s", upid)

	_, err = tasksAPI.Wait(ctx, setup.node, upid, &tasks.WaitOptions{TimeoutSeconds: constants.MediumTaskTimeoutSeconds, IntervalMillis: constants.TaskIntervalMillis})
	if err != nil {
		log.Fatalf("wait start: %v", err)
	}
}

func verifyVM(ctx context.Context, qemuAPI qemu.Service, setup vmSetup) {
	status, err := qemuAPI.Status(ctx, setup.node, setup.vmid)
	if err != nil {
		log.Fatalf("status: %v", err)
	}

	log.Printf("VM status: %v", status)
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
		return nil, fmt.Errorf("failed to resolve symbolic links for path %s: %w", path, err)
	}

	// Ensure the path doesn't contain directory traversal patterns
	if strings.Contains(cleanPath, "..") {
		return nil, errDirectoryTraversal
	}

	content, err := os.ReadFile(cleanPath) // #nosec G304 - path is validated
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", cleanPath, err)
	}

	return content, nil
}
