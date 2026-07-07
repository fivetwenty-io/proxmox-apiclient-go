package compatibility_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/compatibility"
)

func TestParseVersion(t *testing.T) {
	t.Parallel()

	tests := getParseVersionTestCases()
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			runParseVersionTest(t, testCase)
		})
	}
}

type parseVersionTestCase struct {
	name      string
	input     string
	expected  *compatibility.Version
	expectErr bool
}

func getParseVersionTestCases() []parseVersionTestCase {
	return []parseVersionTestCase{
		{
			name:  "PVE 7.4-3",
			input: "7.4-3",
			expected: &compatibility.Version{
				Major: 7,
				Minor: 4,
				Patch: 0,
				Build: "3",
			},
			expectErr: false,
		},
		{
			name:  "PVE 8.0-2",
			input: "8.0-2",
			expected: &compatibility.Version{
				Major: 8,
				Minor: 0,
				Patch: 0,
				Build: "2",
			},
			expectErr: false,
		},
		{
			name:  "PVE 7.3.1",
			input: "7.3.1",
			expected: &compatibility.Version{
				Major: 7,
				Minor: 3,
				Patch: 1,
				Build: "",
			},
			expectErr: false,
		},
		{
			name:  "PVE 6.4",
			input: "6.4",
			expected: &compatibility.Version{
				Major: 6,
				Minor: 4,
				Patch: 0,
				Build: "",
			},
			expectErr: false,
		},
		{
			name:      "Invalid format",
			input:     "invalid",
			expected:  nil,
			expectErr: true,
		},
	}
}

func runParseVersionTest(t *testing.T, testCase parseVersionTestCase) {
	t.Helper()

	version, err := compatibility.ParseVersion(testCase.input)
	if testCase.expectErr {
		if err == nil {
			t.Error("Expected error but got none")
		}

		return
	}

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if !reflect.DeepEqual(version, testCase.expected) {
		t.Errorf("Got %+v, expected %+v", version, testCase.expected)
	}
}

func TestVersionCompare(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		v1       *compatibility.Version
		v2       *compatibility.Version
		expected int
	}{
		{
			name:     "Equal versions",
			v1:       &compatibility.Version{Major: 7, Minor: 4, Patch: 3, Build: ""},
			v2:       &compatibility.Version{Major: 7, Minor: 4, Patch: 3, Build: ""},
			expected: 0,
		},
		{
			name:     "Major version difference",
			v1:       &compatibility.Version{Major: 6, Minor: 4, Patch: 0, Build: ""},
			v2:       &compatibility.Version{Major: 7, Minor: 4, Patch: 0, Build: ""},
			expected: -1,
		},
		{
			name:     "Minor version difference",
			v1:       &compatibility.Version{Major: 7, Minor: 3, Patch: 0, Build: ""},
			v2:       &compatibility.Version{Major: 7, Minor: 4, Patch: 0, Build: ""},
			expected: -1,
		},
		{
			name:     "Patch version difference",
			v1:       &compatibility.Version{Major: 7, Minor: 4, Patch: 1, Build: ""},
			v2:       &compatibility.Version{Major: 7, Minor: 4, Patch: 3, Build: ""},
			expected: -1,
		},
		{
			name:     "Higher version",
			v1:       &compatibility.Version{Major: 8, Minor: 0, Patch: 0, Build: ""},
			v2:       &compatibility.Version{Major: 7, Minor: 4, Patch: 0, Build: ""},
			expected: 1,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			result := testCase.v1.Compare(testCase.v2)
			if result != testCase.expected {
				t.Errorf("Compare() = %d, expected %d", result, testCase.expected)
			}
		})
	}
}

func TestIsAtLeast(t *testing.T) {
	t.Parallel()

	version := &compatibility.Version{Major: 7, Minor: 4, Patch: 3, Build: ""}

	tests := []struct {
		name     string
		major    int
		minor    int
		patch    int
		expected bool
	}{
		{
			name:     "Exact version",
			major:    7,
			minor:    4,
			patch:    3,
			expected: true,
		},
		{
			name:     "Lower version",
			major:    7,
			minor:    3,
			patch:    0,
			expected: true,
		},
		{
			name:     "Higher version",
			major:    7,
			minor:    5,
			patch:    0,
			expected: false,
		},
		{
			name:     "Much lower version",
			major:    6,
			minor:    0,
			patch:    0,
			expected: true,
		},
		{
			name:     "Much higher version",
			major:    8,
			minor:    0,
			patch:    0,
			expected: false,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			result := version.IsAtLeast(testCase.major, testCase.minor, testCase.patch)
			if result != testCase.expected {
				t.Errorf("IsAtLeast(%d, %d, %d) = %v, expected %v",
					testCase.major, testCase.minor, testCase.patch, result, testCase.expected)
			}
		})
	}
}

func TestMatrixFeatureSupport(t *testing.T) {
	t.Parallel()

	matrix := compatibility.NewMatrix()
	tests := getMatrixFeatureSupportTestCases()

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			runMatrixFeatureSupportTest(t, matrix, testCase)
		})
	}
}

type matrixFeatureSupportTestCase struct {
	name       string
	feature    string
	version    *compatibility.Version
	supported  bool
	hasWarning bool
}

func getMatrixFeatureSupportTestCases() []matrixFeatureSupportTestCase {
	cases := getSupportedFeatureTestCases()
	cases = append(cases, getUnsupportedFeatureTestCases()...)
	cases = append(cases, getDeprecatedFeatureTestCases()...)

	return cases
}

const (
	featurePBSIntegration = "pbs_integration"
	featureSDN            = "sdn"
	featureOpenVZ         = "openvz"
)

func getSupportedFeatureTestCases() []matrixFeatureSupportTestCase {
	return []matrixFeatureSupportTestCase{
		{
			name:       "PVE 6.0 supports storage_content",
			feature:    "storage_content",
			version:    &compatibility.Version{Major: 6, Minor: 0, Patch: 0, Build: ""},
			supported:  true,
			hasWarning: false,
		},
		{
			name:       "PVE 6.2 supports cloud_init",
			feature:    "cloud_init",
			version:    &compatibility.Version{Major: 6, Minor: 2, Patch: 0, Build: ""},
			supported:  true,
			hasWarning: false,
		},
		{
			name:       "PVE 7.0 supports PBS integration",
			feature:    featurePBSIntegration,
			version:    &compatibility.Version{Major: 7, Minor: 0, Patch: 0, Build: ""},
			supported:  true,
			hasWarning: false,
		},
		{
			name:       "PVE 7.3 supports SDN",
			feature:    featureSDN,
			version:    &compatibility.Version{Major: 7, Minor: 3, Patch: 0, Build: ""},
			supported:  true,
			hasWarning: false,
		},
		{
			name:       "PVE 8.1 supports notification system",
			feature:    "notification_system",
			version:    &compatibility.Version{Major: 8, Minor: 1, Patch: 0, Build: ""},
			supported:  true,
			hasWarning: false,
		},
	}
}

func getUnsupportedFeatureTestCases() []matrixFeatureSupportTestCase {
	return []matrixFeatureSupportTestCase{
		{
			name:       "PVE 6.1 doesn't support cloud_init",
			feature:    "cloud_init",
			version:    &compatibility.Version{Major: 6, Minor: 1, Patch: 0, Build: ""},
			supported:  false,
			hasWarning: false,
		},
		{
			name:       "PVE 6.4 doesn't support PBS integration",
			feature:    featurePBSIntegration,
			version:    &compatibility.Version{Major: 6, Minor: 4, Patch: 0, Build: ""},
			supported:  false,
			hasWarning: false,
		},
		{
			name:       "PVE 7.2 doesn't support SDN",
			feature:    featureSDN,
			version:    &compatibility.Version{Major: 7, Minor: 2, Patch: 0, Build: ""},
			supported:  false,
			hasWarning: false,
		},
		{
			name:       "OpenVZ not available in PVE 6",
			feature:    featureOpenVZ,
			version:    &compatibility.Version{Major: 6, Minor: 0, Patch: 0, Build: ""},
			supported:  false,
			hasWarning: false,
		},
	}
}

func getDeprecatedFeatureTestCases() []matrixFeatureSupportTestCase {
	return []matrixFeatureSupportTestCase{
		{
			name:       "OpenVZ deprecated in PVE 4",
			feature:    featureOpenVZ,
			version:    &compatibility.Version{Major: 4, Minor: 0, Patch: 0, Build: ""},
			supported:  true,
			hasWarning: true,
		},
	}
}

func runMatrixFeatureSupportTest(t *testing.T, matrix *compatibility.Matrix, testCase matrixFeatureSupportTestCase) {
	t.Helper()

	supported, msg := matrix.IsFeatureSupported(testCase.feature, testCase.version)
	if supported != testCase.supported {
		t.Errorf("IsFeatureSupported(%s, %v) = %v, expected %v",
			testCase.feature, testCase.version, supported, testCase.supported)
	}

	if testCase.hasWarning && msg == "" {
		t.Error("Expected warning message but got none")
	}
}

func TestGetSupportedFeatures(t *testing.T) {
	t.Parallel()

	matrix := compatibility.NewMatrix()

	tests := []struct {
		name     string
		version  *compatibility.Version
		minCount int // Minimum expected features
	}{
		{
			name:     "PVE 6.0",
			version:  &compatibility.Version{Major: 6, Minor: 0, Patch: 0, Build: ""},
			minCount: 3,
		},
		{
			name:     "PVE 7.0",
			version:  &compatibility.Version{Major: 7, Minor: 0, Patch: 0, Build: ""},
			minCount: 6,
		},
		{
			name:     "PVE 7.4",
			version:  &compatibility.Version{Major: 7, Minor: 4, Patch: 0, Build: ""},
			minCount: 10,
		},
		{
			name:     "PVE 8.1",
			version:  &compatibility.Version{Major: 8, Minor: 1, Patch: 0, Build: ""},
			minCount: 14,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			features := matrix.GetSupportedFeatures(testCase.version)
			if len(features) < testCase.minCount {
				t.Errorf("GetSupportedFeatures(%v) returned %d features, expected at least %d",
					testCase.version, len(features), testCase.minCount)
			}
		})
	}
}

func TestChecker(t *testing.T) {
	t.Parallel()

	checker, err := compatibility.NewChecker("7.4-3")
	if err != nil {
		t.Fatalf("NewChecker() error = %v", err)
	}

	// Test version parsing
	version := checker.GetVersion()
	if version.Major != 7 || version.Minor != 4 {
		t.Errorf("GetVersion() = %v, expected 7.4", version)
	}

	// Test feature checking
	tests := []struct {
		feature   string
		supported bool
	}{
		{"pbs_integration", true},
		{"sdn", true},
		{"notification_system", false},
		{"webauthn", true},
		{"openvz", false},
	}

	for _, testCase := range tests {
		supported, _ := checker.Check(testCase.feature)
		if supported != testCase.supported {
			t.Errorf("Check(%s) = %v, expected %v", testCase.feature, supported, testCase.supported)
		}
	}
}

func TestEndpointRegistry(t *testing.T) {
	t.Parallel()

	registry := compatibility.NewEndpointRegistry()

	tests := []struct {
		name        string
		endpoint    string
		version     *compatibility.Version
		expectError bool
	}{
		{
			name:        "VM config available in PVE 7",
			endpoint:    "vm_config",
			version:     &compatibility.Version{Major: 7, Minor: 0, Patch: 0, Build: ""},
			expectError: false,
		},
		{
			name:        "Cloud-init available in PVE 6.2",
			endpoint:    "vm_cloud_init",
			version:     &compatibility.Version{Major: 6, Minor: 2, Patch: 0, Build: ""},
			expectError: false,
		},
		{
			name:        "Cloud-init not available in PVE 6.1",
			endpoint:    "vm_cloud_init",
			version:     &compatibility.Version{Major: 6, Minor: 1, Patch: 0, Build: ""},
			expectError: true,
		},
		{
			name:        "Unknown endpoint",
			endpoint:    "unknown",
			version:     &compatibility.Version{Major: 7, Minor: 0, Patch: 0, Build: ""},
			expectError: true,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			endpoint, err := registry.GetEndpoint(testCase.endpoint, testCase.version)
			if testCase.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}

				if endpoint == nil {
					t.Error("Got nil endpoint")
				}
			}
		})
	}
}

func TestGenerateReport(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		version        string
		hasWarnings    bool
		hasRecommended bool
	}{
		{
			name:           "PVE 6.4 has warnings",
			version:        "6.4-1",
			hasWarnings:    true,
			hasRecommended: true,
		},
		{
			name:           "PVE 7.3 has recommendations",
			version:        "7.3-1",
			hasWarnings:    false,
			hasRecommended: true,
		},
		{
			name:           "PVE 8.1 is current",
			version:        "8.1-1",
			hasWarnings:    false,
			hasRecommended: true,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			report, err := compatibility.GenerateReport(testCase.version)
			if err != nil {
				t.Fatalf("GenerateReport() error = %v", err)
			}

			if testCase.hasWarnings && len(report.Warnings) == 0 {
				t.Error("Expected warnings but got none")
			}

			if testCase.hasRecommended && len(report.Recommendations) == 0 {
				t.Error("Expected recommendations but got none")
			}

			if len(report.SupportedFeatures) == 0 {
				t.Error("No supported features in report")
			}
		})
	}
}

func TestMigrationHelper(t *testing.T) {
	t.Parallel()

	helper, err := compatibility.NewMigrationHelper("6.4-1", "7.4-1")
	if err != nil {
		t.Fatalf("NewMigrationHelper() error = %v", err)
	}

	// Test migration steps
	steps := helper.GetMigrationSteps()
	if len(steps) == 0 {
		t.Error("No migration steps returned")
	}

	// Test new features
	newFeatures := helper.GetNewFeatures()
	if len(newFeatures) == 0 {
		t.Error("No new features found")
	}

	// Test breaking changes
	changes := helper.GetBreakingChanges()
	if len(changes) == 0 {
		t.Error("No breaking changes found for major version upgrade")
	}
}

func TestMigrationHelperGetDeprecatedFeatures(t *testing.T) {
	t.Parallel()

	// openvz is supported through PVE 5.4 and dropped in 6.x+, so a 5.x->8.x
	// migration must report it as deprecated. Regression test: the lookup
	// previously used the feature's display name instead of its matrix key,
	// so the result was always empty.
	helper, err := compatibility.NewMigrationHelper("5.4-1", "8.0-1")
	if err != nil {
		t.Fatalf("NewMigrationHelper() error = %v", err)
	}

	deprecated := helper.GetDeprecatedFeatures()

	if !slices.Contains(deprecated, "OpenVZ Containers") {
		t.Errorf("GetDeprecatedFeatures() = %v, want it to contain %q", deprecated, "OpenVZ Containers")
	}
}

func TestValidateConfiguration(t *testing.T) {
	t.Parallel()

	tests := getValidateConfigurationTestCases()
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			runValidateConfigurationTest(t, testCase)
		})
	}
}

type validateConfigurationTestCase struct {
	name    string
	config  map[string]interface{}
	version *compatibility.Version
	valid   bool
}

func getValidateConfigurationTestCases() []validateConfigurationTestCase {
	return []validateConfigurationTestCase{
		{
			name: "Valid PVE 7 config",
			config: map[string]interface{}{
				"cluster": map[string]interface{}{
					"name": "test-cluster",
				},
			},
			version: &compatibility.Version{Major: 7, Minor: 4, Patch: 0, Build: ""},
			valid:   true,
		},
		{
			name: "PVE 8 without notification config",
			config: map[string]interface{}{
				"cluster": map[string]interface{}{
					"name": "test-cluster",
				},
			},
			version: &compatibility.Version{Major: 8, Minor: 0, Patch: 0, Build: ""},
			valid:   false,
		},
		{
			name: "OpenVZ config in PVE 6",
			config: map[string]interface{}{
				"openvz": map[string]interface{}{
					"enabled": true,
				},
			},
			version: &compatibility.Version{Major: 6, Minor: 0, Patch: 0, Build: ""},
			valid:   false,
		},
		{
			name: "SDN config in PVE 7.2",
			config: map[string]interface{}{
				"sdn": map[string]interface{}{
					"zones": []string{"zone1"},
				},
			},
			version: &compatibility.Version{Major: 7, Minor: 2, Patch: 0, Build: ""},
			valid:   false,
		},
	}
}

func runValidateConfigurationTest(t *testing.T, testCase validateConfigurationTestCase) {
	t.Helper()

	valid, issues := compatibility.ValidateConfiguration(testCase.config, testCase.version)
	if valid != testCase.valid {
		t.Errorf("ValidateConfiguration() valid = %v, expected %v", valid, testCase.valid)
	}

	if !testCase.valid && len(issues) == 0 {
		t.Error("Invalid config but no issues reported")
	}
}

// Benchmark tests.
func BenchmarkParseVersion(b *testing.B) {
	for range b.N {
		_, _ = compatibility.ParseVersion("7.4-3")
	}
}

func BenchmarkVersionCompare(b *testing.B) {
	v1 := &compatibility.Version{Major: 7, Minor: 4, Patch: 3, Build: ""}
	v2 := &compatibility.Version{Major: 7, Minor: 3, Patch: 1, Build: ""}

	for range b.N {
		v1.Compare(v2)
	}
}

func BenchmarkFeatureCheck(b *testing.B) {
	matrix := compatibility.NewMatrix()
	version := &compatibility.Version{Major: 7, Minor: 4, Patch: 0, Build: ""}

	b.ResetTimer()

	for range b.N {
		matrix.IsFeatureSupported("pbs_integration", version)
	}
}

func BenchmarkGetSupportedFeatures(b *testing.B) {
	matrix := compatibility.NewMatrix()
	version := &compatibility.Version{Major: 7, Minor: 4, Patch: 0, Build: ""}

	b.ResetTimer()

	for range b.N {
		matrix.GetSupportedFeatures(version)
	}
}
