package cmd

import (
	"strings"
	"testing"
)

// matrix of common picks; every combination must render without
// template errors and produce sensible YAML.
func TestDeployTemplates_Combinations(t *testing.T) {
	combos := []ScaffoldOptions{
		{Name: "demo", Database: "postgres", Cache: "none", Storage: "none"},
		{Name: "demo", Database: "mysql", Cache: "redis", Storage: "none"},
		{Name: "demo", Database: "sqlite", Cache: "none", Storage: "minio"},
		{Name: "demo", Database: "postgres", Cache: "redis", Storage: "minio"},
		{Name: "demo", Database: "none", Cache: "none", Storage: "none"},
	}
	templates := []struct{ name, body string }{
		{"Dockerfile", dockerfileTmpl},
		{"compose", composeTmpl},
		{"k8s-deployment", k8sDeploymentTmpl},
		{"k8s-service", k8sServiceTmpl},
		{"k8s-configmap", k8sConfigMapTmpl},
		{"k8s-secret", k8sSecretTmpl},
	}
	for _, opts := range combos {
		for _, tpl := range templates {
			out, err := renderTemplate(tpl.name, tpl.body, opts)
			if err != nil {
				t.Fatalf("render %s with %+v: %v", tpl.name, opts, err)
			}
			if strings.TrimSpace(out) == "" {
				t.Fatalf("render %s with %+v: empty output", tpl.name, opts)
			}
		}
	}
}

func TestDockerfile_NonRootDistroless(t *testing.T) {
	out, _ := renderTemplate("Dockerfile", dockerfileTmpl, ScaffoldOptions{Name: "demo"})
	for _, want := range []string{
		"distroless",
		"USER nonroot",
		"CGO_ENABLED=0",
		"-trimpath",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("Dockerfile missing %q:\n%s", want, out)
		}
	}
}

func TestCompose_OnlyIncludesPickedServices(t *testing.T) {
	out, _ := renderTemplate("compose", composeTmpl, ScaffoldOptions{
		Name: "demo", Database: "postgres", Cache: "none", Storage: "none",
	})
	if !strings.Contains(out, "image: postgres:") {
		t.Fatalf("expected postgres service:\n%s", out)
	}
	if strings.Contains(out, "image: redis:") {
		t.Fatalf("unwanted redis service appeared:\n%s", out)
	}
	if strings.Contains(out, "image: minio/") {
		t.Fatalf("unwanted minio service appeared:\n%s", out)
	}
}

func TestK8sDeployment_HasProbesAndSecurityContext(t *testing.T) {
	out, _ := renderTemplate("k8s-deployment", k8sDeploymentTmpl, ScaffoldOptions{Name: "demo"})
	for _, want := range []string{
		"readinessProbe:",
		"livenessProbe:",
		"path: /readyz",
		"path: /healthz",
		"runAsNonRoot: true",
		"readOnlyRootFilesystem: true",
		"drop: [\"ALL\"]",
		"maxUnavailable: 0",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("deployment.yaml missing %q:\n%s", want, out)
		}
	}
}

func TestK8sConfigMap_ConditionalKeys(t *testing.T) {
	out, _ := renderTemplate("k8s-configmap", k8sConfigMapTmpl, ScaffoldOptions{
		Name: "demo", Database: "postgres", Cache: "redis", Storage: "minio",
	})
	for _, want := range []string{
		`DB_CONNECTION: "postgres"`,
		`REDIS_ADDR:`,
		`S3_BUCKET: "demo"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("configmap missing %q:\n%s", want, out)
		}
	}
}

func TestEnvStubWithInfra_AllCombos(t *testing.T) {
	out := envStubWithInfra(ScaffoldOptions{
		Name: "demo", Database: "postgres", Cache: "redis", Storage: "minio",
	})
	for _, want := range []string{
		"APP_NAME=demo",
		"DB_CONNECTION=postgres",
		"REDIS_ADDR=",
		"S3_ENDPOINT=",
		"S3_BUCKET=demo",
		"JWT_SECRET=",
		"APP_KEY=",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf(".env missing %q:\n%s", want, out)
		}
	}

	none := envStubWithInfra(ScaffoldOptions{Name: "demo", Database: "none", Cache: "none", Storage: "none"})
	if strings.Contains(none, "DB_CONNECTION=") || strings.Contains(none, "REDIS_ADDR=") {
		t.Fatalf(".env with no infra should not include infra vars:\n%s", none)
	}
}
