// Package compatibility provides PVE version parsing/comparison, a feature
// support matrix keyed by PVE version ranges, and migration-helper reports
// (new/deprecated features, breaking changes) between two PVE versions.
//
// This is a standalone opt-in utility: it is not wired into pkg/client.Client
// or the pkg/api/* packages, none of which consult it before making a
// request. Use compatibility.NewChecker(pveVersion) directly when your
// application needs to gate behavior on the PVE version it is talking to.
package compatibility

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/constants"
)

// PVE version constants.
const (
	PVEMajorV3 = 3
	PVEMajorV5 = 5
	PVEMajorV6 = 6
	PVEMajorV7 = 7
	PVEMajorV8 = 8

	PVEMinorV0 = 0
	PVEMinorV1 = 1
	PVEMinorV2 = 2
	PVEMinorV3 = 3
	PVEMinorV4 = 4

	PVEPatchV0   = 0
	PVEPatchV999 = 999
)

var (
	ErrInvalidVersionFormat      = errors.New("invalid version format")
	ErrUnknownFeature            = errors.New("unknown feature")
	ErrUnknownEndpoint           = errors.New("unknown endpoint")
	ErrNoCompatibleEndpointFound = errors.New("no compatible endpoint found")
)

// Version represents a PVE version.
type Version struct {
	Major int
	Minor int
	Patch int
	Build string
}

// ParseVersion parses a PVE version string.
func ParseVersion(versionStr string) (*Version, error) {
	// Handle various PVE version formats
	// Examples: "7.4-3", "8.0-2", "7.3-1"
	re := regexp.MustCompile(`^(\d+)\.(\d+)(?:\.(\d+))?(?:-(.+))?$`)
	matches := re.FindStringSubmatch(versionStr)

	if len(matches) < constants.MinimumMatchCount {
		return nil, fmt.Errorf("%w: %s", ErrInvalidVersionFormat, versionStr)
	}

	major, _ := strconv.Atoi(matches[1])
	minor, _ := strconv.Atoi(matches[2])

	patch := 0
	if matches[3] != "" {
		patch, _ = strconv.Atoi(matches[3])
	}

	return &Version{
		Major: major,
		Minor: minor,
		Patch: patch,
		Build: matches[4],
	}, nil
}

// String returns the string representation of the version.
func (v *Version) String() string {
	if v.Build != "" {
		return fmt.Sprintf("%d.%d.%d-%s", v.Major, v.Minor, v.Patch, v.Build)
	}

	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// Compare compares two versions.
// Returns -1 if v < other, 0 if v == other, 1 if v > other.
func (v *Version) Compare(other *Version) int {
	if v.Major != other.Major {
		if v.Major < other.Major {
			return -1
		}

		return 1
	}

	if v.Minor != other.Minor {
		if v.Minor < other.Minor {
			return -1
		}

		return 1
	}

	if v.Patch != other.Patch {
		if v.Patch < other.Patch {
			return -1
		}

		return 1
	}

	return 0
}

// IsAtLeast checks if version is at least the specified version.
func (v *Version) IsAtLeast(major, minor, patch int) bool {
	other := &Version{
		Major: major,
		Minor: minor,
		Patch: patch,
		Build: "",
	}

	return v.Compare(other) >= 0
}

// Feature represents a PVE API feature.
type Feature struct {
	Name        string
	Description string
	MinVersion  *Version
	MaxVersion  *Version // nil means no upper limit
	Deprecated  bool
	Alternative string // Alternative feature or method if deprecated
}

// Matrix provides compatibility information for different PVE versions.
type Matrix struct {
	features map[string]*Feature
}

// NewMatrix creates a new compatibility matrix.
func NewMatrix() *Matrix {
	matrix := &Matrix{
		features: make(map[string]*Feature),
	}

	// Initialize with known PVE features and their version requirements
	matrix.initializeFeatures()

	return matrix
}

// AddFeature adds a feature to the compatibility matrix.
func (m *Matrix) AddFeature(key string, feature *Feature) {
	m.features[key] = feature
}

// IsFeatureSupported checks if a feature is supported in a given PVE version.
func (m *Matrix) IsFeatureSupported(featureKey string, version *Version) (bool, string) {
	feature, exists := m.features[featureKey]
	if !exists {
		return false, "unknown feature: " + featureKey
	}

	// Check minimum version
	if feature.MinVersion != nil && version.Compare(feature.MinVersion) < 0 {
		return false, fmt.Sprintf("feature %s requires PVE %s or later", feature.Name, feature.MinVersion)
	}

	// Check maximum version (for deprecated features)
	if feature.MaxVersion != nil && version.Compare(feature.MaxVersion) > 0 {
		msg := fmt.Sprintf("feature %s is not available in PVE %s", feature.Name, version)
		if feature.Alternative != "" {
			msg += fmt.Sprintf(" (%s)", feature.Alternative)
		}

		return false, msg
	}

	// Check if deprecated
	if feature.Deprecated {
		msg := fmt.Sprintf("feature %s is deprecated", feature.Name)
		if feature.Alternative != "" {
			msg += fmt.Sprintf(" (%s)", feature.Alternative)
		}

		return true, msg
	}

	return true, ""
}

// GetSupportedFeatures returns all features supported in a given PVE version.
func (m *Matrix) GetSupportedFeatures(version *Version) []string {
	var supported []string

	for key := range m.features {
		if ok, _ := m.IsFeatureSupported(key, version); ok {
			supported = append(supported, key)
		}
	}

	return supported
}

// GetFeatureInfo returns information about a specific feature.
func (m *Matrix) GetFeatureInfo(featureKey string) (*Feature, error) {
	feature, exists := m.features[featureKey]
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrUnknownFeature, featureKey)
	}

	return feature, nil
}

func (m *Matrix) initializeFeatures() {
	m.initializeCoreFeatures()
	m.initializeAuthFeatures()
	m.initializeBackupFeatures()
	m.initializeMigrationFeatures()
	m.initializeStorageFeatures()
	m.initializeHardwareFeatures()
	m.initializeDeprecatedFeatures()
}

func (m *Matrix) initializeCoreFeatures() {
	m.addCoreStorageFeatures()
	m.addCoreVMFeatures()
	m.addCoreNetworkingFeatures()
	m.addCoreSystemFeatures()
}

func (m *Matrix) addCoreStorageFeatures() {
	m.AddFeature("storage_content", &Feature{
		Name:        "Storage Content API",
		Description: "Access to storage content listing and management",
		MinVersion:  &Version{Major: PVEMajorV6, Minor: PVEMinorV0, Patch: PVEPatchV0, Build: ""},
		MaxVersion:  nil,
		Deprecated:  false,
		Alternative: "",
	})

	m.AddFeature("pbs_integration", &Feature{
		Name:        "Proxmox Backup Server Integration",
		Description: "Native PBS backup and restore support",
		MinVersion:  &Version{Major: PVEMajorV7, Minor: PVEMinorV0, Patch: PVEPatchV0, Build: ""},
		MaxVersion:  nil,
		Deprecated:  false,
		Alternative: "",
	})
}

func (m *Matrix) addCoreVMFeatures() {
	m.AddFeature("vm_snapshots", &Feature{
		Name:        "VM Snapshots",
		Description: "VM snapshot creation and management",
		MinVersion:  &Version{Major: PVEMajorV6, Minor: PVEMinorV0, Patch: PVEPatchV0, Build: ""},
		MaxVersion:  nil,
		Deprecated:  false,
		Alternative: "",
	})

	m.AddFeature("cloud_init", &Feature{
		Name:        "Cloud-Init Support",
		Description: "Cloud-init configuration for VMs",
		MinVersion:  &Version{Major: PVEMajorV6, Minor: PVEMinorV2, Patch: PVEPatchV0, Build: ""},
		MaxVersion:  nil,
		Deprecated:  false,
		Alternative: "",
	})

	m.AddFeature("tags", &Feature{
		Name:        "VM/CT Tags",
		Description: "Tagging support for VMs and containers",
		MinVersion:  &Version{Major: PVEMajorV7, Minor: PVEMinorV1, Patch: PVEPatchV0, Build: ""},
		MaxVersion:  nil,
		Deprecated:  false,
		Alternative: "",
	})
}

func (m *Matrix) addCoreNetworkingFeatures() {
	m.AddFeature("sdn", &Feature{
		Name:        "Software Defined Networking",
		Description: "SDN zones, vnets, and subnets",
		MinVersion:  &Version{Major: PVEMajorV7, Minor: PVEMinorV3, Patch: PVEPatchV0, Build: ""},
		MaxVersion:  nil,
		Deprecated:  false,
		Alternative: "",
	})

	m.AddFeature("firewall_ipsets_v2", &Feature{
		Name:        "Firewall IPSets v2",
		Description: "Enhanced firewall IP sets with additional options",
		MinVersion:  &Version{Major: PVEMajorV8, Minor: PVEMinorV0, Patch: PVEPatchV0, Build: ""},
		MaxVersion:  nil,
		Deprecated:  false,
		Alternative: "",
	})
}

func (m *Matrix) addCoreSystemFeatures() {
	m.AddFeature("notification_system", &Feature{
		Name:        "Notification System",
		Description: "Unified notification system with matchers and targets",
		MinVersion:  &Version{Major: PVEMajorV8, Minor: PVEMinorV1, Patch: PVEPatchV0, Build: ""},
		MaxVersion:  nil,
		Deprecated:  false,
		Alternative: "",
	})

	m.AddFeature("pool_permissions", &Feature{
		Name:        "Enhanced Pool Permissions",
		Description: "Granular permission management for resource pools",
		MinVersion:  &Version{Major: PVEMajorV6, Minor: PVEMinorV1, Patch: PVEPatchV0, Build: ""},
		MaxVersion:  nil,
		Deprecated:  false,
		Alternative: "",
	})
}

func (m *Matrix) initializeAuthFeatures() {
	m.AddFeature("api_token_auth", &Feature{
		Name:        "API Token Authentication",
		Description: "Token-based authentication for API access",
		MinVersion:  &Version{Major: PVEMajorV6, Minor: PVEMinorV2, Patch: PVEPatchV0, Build: ""},
		MaxVersion:  nil,
		Deprecated:  false,
		Alternative: "",
	})

	m.AddFeature("webauthn", &Feature{
		Name:        "WebAuthn/FIDO2",
		Description: "WebAuthn/FIDO2 second factor authentication",
		MinVersion:  &Version{Major: PVEMajorV7, Minor: PVEMinorV4, Patch: PVEPatchV0, Build: ""},
		MaxVersion:  nil,
		Deprecated:  false,
		Alternative: "",
	})
}

func (m *Matrix) initializeBackupFeatures() {
	m.AddFeature("backup_fleecing", &Feature{
		Name:        "Backup Fleecing",
		Description: "Improved backup performance using fleecing",
		MinVersion:  &Version{Major: PVEMajorV7, Minor: PVEMinorV2, Patch: PVEPatchV0, Build: ""},
		MaxVersion:  nil,
		Deprecated:  false,
		Alternative: "",
	})

	m.AddFeature("backup_notes", &Feature{
		Name:        "Backup Notes",
		Description: "Notes field for backup archives",
		MinVersion:  &Version{Major: PVEMajorV6, Minor: PVEMinorV3, Patch: PVEPatchV0, Build: ""},
		MaxVersion:  nil,
		Deprecated:  false,
		Alternative: "",
	})
}

func (m *Matrix) initializeMigrationFeatures() {
	m.AddFeature("live_migration_nbd", &Feature{
		Name:        "NBD Live Migration",
		Description: "Live migration using NBD protocol",
		MinVersion:  &Version{Major: PVEMajorV6, Minor: PVEMinorV0, Patch: PVEPatchV0, Build: ""},
		MaxVersion:  nil,
		Deprecated:  false,
		Alternative: "",
	})

	m.AddFeature("remote_migration", &Feature{
		Name:        "Remote Migration",
		Description: "Migration between different PVE clusters",
		MinVersion:  &Version{Major: PVEMajorV7, Minor: PVEMinorV0, Patch: PVEPatchV0, Build: ""},
		MaxVersion:  nil,
		Deprecated:  false,
		Alternative: "",
	})
}

func (m *Matrix) initializeStorageFeatures() {
	m.AddFeature("ceph_quincy", &Feature{
		Name:        "Ceph Quincy",
		Description: "Ceph Quincy (17.x) support",
		MinVersion:  &Version{Major: PVEMajorV7, Minor: PVEMinorV2, Patch: PVEPatchV0, Build: ""},
		MaxVersion:  nil,
		Deprecated:  false,
		Alternative: "",
	})

	m.AddFeature("ceph_reef", &Feature{
		Name:        "Ceph Reef",
		Description: "Ceph Reef (18.x) support",
		MinVersion:  &Version{Major: PVEMajorV8, Minor: PVEMinorV1, Patch: PVEPatchV0, Build: ""},
		MaxVersion:  nil,
		Deprecated:  false,
		Alternative: "",
	})
}

func (m *Matrix) initializeHardwareFeatures() {
	m.AddFeature("pci_mapping", &Feature{
		Name:        "PCI Device Mapping",
		Description: "Cluster-wide PCI device mapping",
		MinVersion:  &Version{Major: PVEMajorV8, Minor: PVEMinorV1, Patch: PVEPatchV0, Build: ""},
		MaxVersion:  nil,
		Deprecated:  false,
		Alternative: "",
	})

	m.AddFeature("cpu_models_v2", &Feature{
		Name:        "CPU Models v2",
		Description: "Enhanced CPU model definitions",
		MinVersion:  &Version{Major: PVEMajorV7, Minor: PVEMinorV3, Patch: PVEPatchV0, Build: ""},
		MaxVersion:  nil,
		Deprecated:  false,
		Alternative: "",
	})
}

func (m *Matrix) initializeDeprecatedFeatures() {
	m.AddFeature("openvz", &Feature{
		Name:        "OpenVZ Containers",
		Description: "OpenVZ container support",
		MinVersion:  &Version{Major: PVEMajorV3, Minor: PVEMinorV0, Patch: PVEPatchV0, Build: ""},
		MaxVersion:  &Version{Major: PVEMajorV5, Minor: PVEMinorV4, Patch: PVEPatchV999, Build: ""},
		Deprecated:  true,
		Alternative: "Use LXC containers instead",
	})
}

// Checker provides compatibility checking functionality.
type Checker struct {
	matrix     *Matrix
	pveVersion *Version
}

// NewChecker creates a new compatibility checker.
func NewChecker(pveVersion string) (*Checker, error) {
	version, err := ParseVersion(pveVersion)
	if err != nil {
		return nil, fmt.Errorf("failed to parse PVE version: %w", err)
	}

	return &Checker{
		matrix:     NewMatrix(),
		pveVersion: version,
	}, nil
}

// Check checks if a feature is supported.
func (c *Checker) Check(featureKey string) (bool, string) {
	return c.matrix.IsFeatureSupported(featureKey, c.pveVersion)
}

// GetVersion returns the PVE version.
func (c *Checker) GetVersion() *Version {
	return c.pveVersion
}

// GetSupportedFeatures returns all supported features.
func (c *Checker) GetSupportedFeatures() []string {
	return c.matrix.GetSupportedFeatures(c.pveVersion)
}

// APIEndpoint represents an API endpoint with version-specific variations.
type APIEndpoint struct {
	Path        string
	Method      string
	MinVersion  *Version
	MaxVersion  *Version
	Replacement string // Path to use if this endpoint is deprecated
}

// EndpointRegistry maintains API endpoint compatibility information.
type EndpointRegistry struct {
	endpoints map[string][]*APIEndpoint
}

// NewEndpointRegistry creates a new endpoint registry.
func NewEndpointRegistry() *EndpointRegistry {
	r := &EndpointRegistry{
		endpoints: make(map[string][]*APIEndpoint),
	}
	r.initialize()

	return r
}

// Register registers an API endpoint.
func (r *EndpointRegistry) Register(key string, endpoint *APIEndpoint) {
	r.endpoints[key] = append(r.endpoints[key], endpoint)
}

// GetEndpoint returns the appropriate endpoint for a given version.
func (r *EndpointRegistry) GetEndpoint(key string, version *Version) (*APIEndpoint, error) {
	endpoints, exists := r.endpoints[key]
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrUnknownEndpoint, key)
	}

	for _, endpoint := range endpoints {
		// Check version compatibility
		if endpoint.MinVersion != nil && version.Compare(endpoint.MinVersion) < 0 {
			continue
		}

		if endpoint.MaxVersion != nil && version.Compare(endpoint.MaxVersion) > 0 {
			continue
		}

		return endpoint, nil
	}

	return nil, fmt.Errorf("%w for %s in PVE %s", ErrNoCompatibleEndpointFound, key, version)
}

func (r *EndpointRegistry) initialize() {
	// Example endpoint versioning
	r.Register("vm_config", &APIEndpoint{
		Path:        "/nodes/{node}/qemu/{vmid}/config",
		Method:      "GET",
		MinVersion:  &Version{Major: PVEMajorV6, Minor: PVEMinorV0, Patch: PVEPatchV0, Build: ""},
		MaxVersion:  nil,
		Replacement: "",
	})

	r.Register("vm_cloud_init", &APIEndpoint{
		Path:        "/nodes/{node}/qemu/{vmid}/cloudinit",
		Method:      "GET",
		MinVersion:  &Version{Major: PVEMajorV6, Minor: PVEMinorV2, Patch: PVEPatchV0, Build: ""},
		MaxVersion:  nil,
		Replacement: "",
	})

	// Deprecated endpoint example
	r.Register("old_backup", &APIEndpoint{
		Path:        "/nodes/{node}/vzdump",
		Method:      "POST",
		MinVersion:  &Version{Major: PVEMajorV3, Minor: PVEMinorV0, Patch: PVEPatchV0, Build: ""},
		MaxVersion:  &Version{Major: PVEMajorV5, Minor: PVEMinorV4, Patch: PVEPatchV999, Build: ""},
		Replacement: "/nodes/{node}/backup",
	})
}

// Report generates a compatibility report.
type Report struct {
	PVEVersion        *Version
	SupportedFeatures []string
	DeprecatedInUse   []string
	Warnings          []string
	Recommendations   []string
}

// GenerateReport generates a compatibility report for a PVE version.
func GenerateReport(pveVersion string) (*Report, error) {
	checker, err := NewChecker(pveVersion)
	if err != nil {
		return nil, err
	}

	report := &Report{
		PVEVersion:        checker.GetVersion(),
		SupportedFeatures: checker.GetSupportedFeatures(),
		DeprecatedInUse:   []string{},
		Warnings:          []string{},
		Recommendations:   []string{},
	}

	// Add version-specific recommendations
	version := checker.GetVersion()

	if version.Major < PVEMajorV7 {
		report.Warnings = append(report.Warnings,
			"PVE 6.x is approaching end of life. Consider upgrading to PVE 7.x or 8.x")
		report.Recommendations = append(report.Recommendations,
			"Plan migration to PVE 7.x or 8.x for continued support and new features")
	}

	if version.Major == PVEMajorV7 && version.Minor < PVEMinorV4 {
		report.Recommendations = append(report.Recommendations,
			"Consider upgrading to PVE 7.4 or later for improved performance and security features")
	}

	if version.Major >= PVEMajorV8 {
		report.Recommendations = append(report.Recommendations,
			"PVE 8.x includes the new notification system and enhanced SDN features")
	}

	// Check for deprecated features
	matrix := NewMatrix()
	for key, feature := range matrix.features {
		if feature.Deprecated {
			if supported, _ := matrix.IsFeatureSupported(key, version); supported {
				report.DeprecatedInUse = append(report.DeprecatedInUse, feature.Name)
				if feature.Alternative != "" {
					report.Recommendations = append(report.Recommendations, feature.Alternative)
				}
			}
		}
	}

	return report, nil
}

// MigrationHelper provides assistance for migrating from older API versions.
type MigrationHelper struct {
	sourceVersion *Version
	targetVersion *Version
	matrix        *Matrix
}

// NewMigrationHelper creates a new migration helper.
func NewMigrationHelper(sourceVersion, targetVersion string) (*MigrationHelper, error) {
	source, err := ParseVersion(sourceVersion)
	if err != nil {
		return nil, fmt.Errorf("invalid source version: %w", err)
	}

	target, err := ParseVersion(targetVersion)
	if err != nil {
		return nil, fmt.Errorf("invalid target version: %w", err)
	}

	return &MigrationHelper{
		sourceVersion: source,
		targetVersion: target,
		matrix:        NewMatrix(),
	}, nil
}

// GetMigrationSteps returns migration steps between versions.
func (h *MigrationHelper) GetMigrationSteps() []string {
	var steps []string

	// Check for major version changes
	if h.sourceVersion.Major < h.targetVersion.Major {
		for major := h.sourceVersion.Major + 1; major <= h.targetVersion.Major; major++ {
			steps = append(steps, h.getMajorVersionSteps(major)...)
		}
	}

	return steps
}

// GetNewFeatures returns features added between versions.
func (h *MigrationHelper) GetNewFeatures() []string {
	var newFeatures []string

	for key, feature := range h.matrix.features {
		// Feature is new if it's supported in target but not in source
		supportedInSource, _ := h.matrix.IsFeatureSupported(key, h.sourceVersion)
		supportedInTarget, _ := h.matrix.IsFeatureSupported(key, h.targetVersion)

		if !supportedInSource && supportedInTarget {
			newFeatures = append(newFeatures, feature.Name)
		}
	}

	return newFeatures
}

// GetDeprecatedFeatures returns features deprecated between versions.
func (h *MigrationHelper) GetDeprecatedFeatures() []string {
	var deprecated []string

	for key, feature := range h.matrix.features {
		if feature.Deprecated {
			// Was supported in source but not in target. Look up by the matrix
			// key (e.g. "openvz"), not feature.Name ("OpenVZ Containers");
			// IsFeatureSupported is keyed on the former, as GetNewFeatures does.
			supportedInSource, _ := h.matrix.IsFeatureSupported(key, h.sourceVersion)
			supportedInTarget, _ := h.matrix.IsFeatureSupported(key, h.targetVersion)

			if supportedInSource && !supportedInTarget {
				deprecated = append(deprecated, feature.Name)
			}
		}
	}

	return deprecated
}

// GetBreakingChanges returns breaking changes between versions.
func (h *MigrationHelper) GetBreakingChanges() []string {
	var changes []string

	// Major version upgrades typically have breaking changes
	if h.targetVersion.Major > h.sourceVersion.Major {
		switch h.targetVersion.Major {
		case PVEMajorV7:
			if h.sourceVersion.Major < PVEMajorV7 {
				changes = append(changes, "Corosync 3 is now required for cluster communication")
				changes = append(changes, "Minimum kernel version changed to 5.x")
				changes = append(changes, "OpenVZ support completely removed")
			}
		case PVEMajorV8:
			if h.sourceVersion.Major < PVEMajorV8 {
				changes = append(changes, "Notification system replaces email-only notifications")
				changes = append(changes, "IPSet API v2 has different parameter structure")
				changes = append(changes, "Some perl modules replaced with rust implementations")
			}
		}
	}

	return changes
}

func (h *MigrationHelper) getMajorVersionSteps(major int) []string {
	var steps []string

	switch major {
	case PVEMajorV7:
		steps = append(steps, "Review PBS integration configuration")
		steps = append(steps, "Update backup scripts to use new PBS features")
		steps = append(steps, "Test remote migration functionality")

	case PVEMajorV8:
		steps = append(steps, "Configure new notification system")
		steps = append(steps, "Review firewall IPSet configurations")
		steps = append(steps, "Test PCI device mapping features")
	}

	return steps
}

// ValidateConfiguration validates if a configuration is compatible with a PVE version.
func ValidateConfiguration(config map[string]interface{}, version *Version) (bool, []string) {
	var issues []string

	// Check for version-specific configuration requirements
	if version.Major >= PVEMajorV8 {
		// PVE 8.x specific validations
		if _, hasNotification := config["notification"]; !hasNotification {
			issues = append(issues, "PVE 8.x requires notification configuration")
		}
	}

	if version.Major >= PVEMajorV7 {
		// PVE 7.x specific validations
		if sdn, ok := config["sdn"].(map[string]interface{}); ok {
			if version.Minor < PVEMinorV3 && len(sdn) > 0 {
				issues = append(issues, "SDN features require PVE 7.3 or later")
			}
		}
	}

	// Check for deprecated configuration options
	if _, hasOpenvz := config["openvz"]; hasOpenvz && version.Major >= PVEMajorV6 {
		issues = append(issues, "OpenVZ configuration is not supported in PVE 6.x and later")
	}

	return len(issues) == 0, issues
}
