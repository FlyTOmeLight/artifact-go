package forge_test

import (
	"bytes"
	"crypto"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type resource map[string]any

type rendered struct {
	resources map[string]resource
	byKind    map[string][]resource
}

func TestHelmRender(t *testing.T) {
	for _, tc := range []struct {
		release, namespace string
	}{
		{release: "alpha", namespace: "north"},
		{release: "beta", namespace: "south"},
	} {
		t.Run(tc.release+"-"+tc.namespace, func(t *testing.T) {
			r := renderForge(t, tc.release, tc.namespace)
			assertUniqueResources(t, r)
			assertReferencesResolve(t, r)
			assertSeedDerivations(t, r, tc.release)
			assertTLS(t, r, tc.release, tc.namespace)
			assertWorkloadContract(t, r, tc.release)
			assertRoleScope(t, r, tc.release)
			assertBootstrapContract(t, r, tc.release, tc.namespace)
		})
	}
}

func renderForge(t *testing.T, release, namespace string) rendered {
	t.Helper()
	output, stderr, err := helmTemplate(t, release, namespace)
	if err != nil {
		t.Fatalf("helm template failed: %v (stderr: %s)", err, safeHelmError(stderr))
	}
	decoder := yaml.NewDecoder(bytes.NewReader(output))
	r := rendered{resources: make(map[string]resource), byKind: make(map[string][]resource)}
	for {
		var doc map[string]any
		if err := decoder.Decode(&doc); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("decode rendered YAML: %v", err)
		}

		if len(doc) == 0 {
			continue
		}
		kind, _ := doc["kind"].(string)
		metadata := mapValue(doc["metadata"])
		name, _ := metadata["name"].(string)
		key := kind + "/" + name
		r.resources[key] = doc
		r.byKind[kind] = append(r.byKind[kind], doc)
	}
	return r
}

func helmTemplate(t *testing.T, release, namespace string) ([]byte, string, error) {
	t.Helper()
	cmd := exec.Command(
		"helm",
		"template",
		release,
		filepath.Join("."),
		"--namespace",
		namespace,
		"--values",
		"values-orbstack.yaml",
		"--set-string",
		"global.forge.external.caPEM=-----BEGIN CERTIFICATE-----\\nZmFrZQ==\\n-----END CERTIFICATE-----",
		"--set",
		"bootstrap.existingSecret=fixture-admin",
		"--set",
		"global.forge.external.caSecret=",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	return output, stderr.String(), err
}

func TestRejectsReleaseNamesThatWouldTruncateOrExceedLimits(t *testing.T) {
	for _, tc := range []struct {
		name, release, namespace, want string
	}{
		{
			name:      "43-character release",
			release:   strings.Repeat("r", 43),
			namespace: "forge",
			want:      "release name",
		},
		{
			name:      "37-character release leaves no room for the Job revision",
			release:   strings.Repeat("r", 37),
			namespace: "forge",
			want:      "release name",
		},
		{
			name:      "release and namespace exceed the management application limit",
			release:   strings.Repeat("r", 36),
			namespace: strings.Repeat("n", 16),
			want:      "release name",
		},
		{
			name:      "namespace alone exceeds the management application limit",
			release:   "forge",
			namespace: strings.Repeat("n", 48),
			want:      "namespace",
		},
		{
			name:      "namespace too short for bootstrap identities",
			release:   "forge",
			namespace: "n",
			want:      "namespace",
		},
		{
			name:      "release with a dot",
			release:   "forge.prod",
			namespace: "forge",
			want:      "release name",
		},
		{
			name:      "release that cannot prefix resource names",
			release:   "1forge",
			namespace: "forge",
			want:      "release name",
		},
		{
			name:      "namespace that cannot prefix bootstrap identities",
			release:   "forge",
			namespace: "1forge",
			want:      "namespace",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, stderr, err := helmTemplate(t, tc.release, tc.namespace)
			if err == nil {
				t.Fatalf("helm template accepted release %q in namespace %q", tc.release, tc.namespace)
			}
			if len(output) != 0 {
				t.Errorf("helm template rejected the names but still rendered output")
			}
			if !strings.Contains(stderr, "forge: ") || !strings.Contains(stderr, tc.want) {
				t.Errorf(
					"render error %q does not name the rejected field %q",
					safeHelmError(stderr),
					tc.want,
				)
			}
			for _, secret := range []string{
				"TENANT_SERVICE_TOKEN",
				"tenant-password",
				"ARTIFACT_DATABASE_URL",
				"BEGIN CERTIFICATE",
			} {
				if strings.Contains(stderr, secret) {
					t.Errorf("render error exposes %q", secret)
				}
			}
		})
	}
}

func TestAcceptsDerivedNameBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, release, namespace string
	}{
		{name: "36-character release", release: strings.Repeat("r", 36), namespace: "forge"},
		{name: "combined length 51", release: strings.Repeat("r", 36), namespace: strings.Repeat("n", 15)},
		{name: "single-character release", release: "f", namespace: "nn"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := renderForge(t, tc.release, tc.namespace)
			assertUniqueResources(t, r)
			assertReferencesResolve(t, r)
			assertBootstrapContract(t, r, tc.release, tc.namespace)
			checkDerivedNameLengths(t, r, tc.release, tc.namespace)
		})
	}
}

// checkDerivedNameLengths measures the rendered resources and the identities
// rendered into the bootstrap ConfigMap, so a boundary acceptance cannot hide
// a name that Kubernetes or the tenant CLI would reject.
func checkDerivedNameLengths(t *testing.T, r rendered, release, namespace string) {
	t.Helper()
	// Kubernetes DNS-1123 subdomain limit for names created by this chart.
	for key := range r.resources {
		name := strings.SplitN(key, "/", 2)[1]
		if len(name) > 63 {
			t.Errorf("resource %s has a %d-character name", key, len(name))
		}
	}
	configMap := r.resources["ConfigMap/"+release+"-external-bootstrap"]
	config := parseJSON(t, stringValue(mapValue(configMap["data"])["config.json"]))
	for _, key := range []string{"Namespace", "OutputSecret", "OIDCApplication", "ManagementApplication", "GitLabTokenName"} {
		value := stringValue(config[key])
		if len(value) < 2 || len(value) > 63 {
			t.Errorf("bootstrap identity %s is %d characters; want 2-63", key, len(value))
		}
	}
	seed := secretData(t, r, release+"-identity-seed")
	for _, key := range []string{"OIDC_CLIENT_ID", "CASDOOR_CLIENT_ID"} {
		if len(seed[key]) > 63 {
			t.Errorf("identity seed %s is %d characters; want at most 63", key, len(seed[key]))
		}
	}
	runtimeConfig := r.resources["ConfigMap/"+release+"-artifact-runtime"]
	artifactConfig := parseJSON(t, stringValue(mapValue(runtimeConfig["data"])["config.json"]))
	if consumer := stringValue(artifactConfig["StorageConsumerID"]); len(consumer) > 64 {
		t.Errorf("StorageConsumerID is %d characters; want at most 64", len(consumer))
	}
}

// Do not include Helm output or arbitrary command stderr in test failures.
func safeHelmError(stderr string) string {
	if strings.TrimSpace(stderr) == "" {
		return "no diagnostic"
	}
	return "Helm returned a render error; details omitted to avoid exposing rendered values"
}

func assertUniqueResources(t *testing.T, r rendered) {
	t.Helper()
	seen := make(map[string]bool)
	for _, kindResources := range r.byKind {
		for _, doc := range kindResources {
			key := objectKey(doc)
			if seen[key] {
				t.Errorf("duplicate resource %s", key)
			}
			seen[key] = true
		}
	}
	if len(r.resources) != len(seen) {
		t.Errorf("resource index has %d entries for %d unique resources", len(r.resources), len(seen))
	}
}

func assertReferencesResolve(t *testing.T, r rendered) {
	t.Helper()
	secrets, configMaps, claims := nameSet(
		r.byKind["Secret"],
	), nameSet(
		r.byKind["ConfigMap"],
	), nameSet(
		r.byKind["PersistentVolumeClaim"],
	)
	claims["fixture-admin"] = true
	secrets["fixture-admin"] = true
	for _, statefulSet := range r.byKind["StatefulSet"] {
		name := stringValue(mapValue(statefulSet["metadata"])["name"])
		for _, claimTemplate := range sliceMaps(mapValue(statefulSet["spec"])["volumeClaimTemplates"]) {
			claimName := stringValue(mapValue(claimTemplate["metadata"])["name"])
			claims[claimName+"-"+name+"-0"] = true
		}
	}
	for _, kind := range []string{"Deployment", "StatefulSet", "Job"} {
		for _, doc := range r.byKind[kind] {
			meta := mapValue(doc["metadata"])
			pod := podSpec(doc)
			if pod == nil {
				t.Errorf("%s has no pod template", objectKey(doc))
				continue
			}
			name := stringValue(meta["name"])
			walkMaps(pod, func(m map[string]any) {
				if ref := mapValue(m["secretRef"]); len(ref) > 0 {
					requireKnown(t, kind+"/"+name+" secretRef", stringValue(ref["name"]), secrets)
				}
				if ref := mapValue(m["configMapRef"]); len(ref) > 0 {
					requireKnown(
						t,
						kind+"/"+name+" configMapRef",
						stringValue(ref["name"]),
						configMaps,
					)
				}
				if ref := mapValue(m["secret"]); len(ref) > 0 {
					requireKnown(
						t,
						kind+"/"+name+" secret volume",
						stringValue(ref["secretName"]),
						secrets,
					)
				}
				if ref := mapValue(m["configMap"]); len(ref) > 0 {
					requireKnown(
						t,
						kind+"/"+name+" configMap volume",
						stringValue(ref["name"]),
						configMaps,
					)
				}
				if ref := mapValue(m["persistentVolumeClaim"]); len(ref) > 0 {
					requireKnown(
						t,
						kind+"/"+name+" PVC volume",
						stringValue(ref["claimName"]),
						claims,
					)
				}
			})
		}
	}
	for _, svc := range r.byKind["Service"] {
		selector := mapValue(mapValue(svc["spec"])["selector"])
		if len(selector) == 0 {
			continue
		}
		matched := false
		for _, kind := range []string{"Deployment", "StatefulSet"} {
			for _, workload := range r.byKind[kind] {
				labels := mapValue(
					mapValue(mapValue(mapValue(workload["spec"])["template"])["metadata"])["labels"],
				)
				if containsLabels(labels, selector) {
					matched = true
				}
			}
		}
		if !matched {
			t.Errorf("Service %s selector matches no rendered workload", objectKey(svc))
		}
	}
}

func assertSeedDerivations(t *testing.T, r rendered, release string) {
	t.Helper()
	seed := secretData(t, r, release+"-seed")
	for _, key := range []string{"artifact-admin-password", "artifact-password", "storage-password", "tenant-password", "tenant-encryption-key", "tenant-service-key", "storage-management-token", "s3-user", "s3-password", "sts-user", "sts-password", "oidc-secret", "casdoor-secret", "bootstrap-password"} {
		if seed[key] == "" {
			t.Errorf("seed key %q is empty", key)
		}
	}
	serviceKeyBytes, err := base64.RawURLEncoding.DecodeString(
		strings.TrimPrefix(seed["tenant-service-key"], "ftk_"),
	)
	if err != nil || len(serviceKeyBytes) != 32 || !strings.HasPrefix(seed["tenant-service-key"], "ftk_") {
		t.Errorf("tenant service key is not ftk_ plus 32 raw URL-base64 bytes")
	}
	assertSecretValues(
		t,
		r,
		release+"-artifact-db",
		map[string]string{"POSTGRES_PASSWORD": seed["artifact-admin-password"]},
	)
	assertSecretValues(
		t,
		r,
		release+"-artifact-db-init",
		map[string]string{
			"01-databases.sql": fmt.Sprintf(
				"CREATE USER artifact_app WITH PASSWORD '%s';\nCREATE USER storage_app WITH PASSWORD '%s';\nCREATE DATABASE artifact OWNER artifact_app;\nCREATE DATABASE storage OWNER storage_app;\n",
				seed["artifact-password"],
				seed["storage-password"],
			),
		},
	)
	assertSecretValues(
		t,
		r,
		release+"-artifact-env",
		map[string]string{
			"STS_ACCESS_KEY_ID":       seed["sts-user"],
			"STS_SECRET_ACCESS_KEY":   seed["sts-password"],
			"TENANT_SERVICE_TOKEN":    seed["tenant-service-key"],
			"TENANT_MANAGEMENT_TOKEN": seed["storage-management-token"],
			"S3_ACCESS_KEY_ID":        seed["s3-user"],
			"S3_SECRET_ACCESS_KEY":    seed["s3-password"],
		},
	)
	assertSecretValues(
		t,
		r,
		release+"-tenant-env",
		map[string]string{
			"POSTGRES_PASSWORD":        seed["tenant-password"],
			"TENANT_ENCRYPTION_KEY":    seed["tenant-encryption-key"],
			"STORAGE_MANAGEMENT_TOKEN": seed["storage-management-token"],
		},
	)
	assertSecretValues(
		t,
		r,
		release+"-storage-credentials",
		map[string]string{"MINIO_ROOT_USER": seed["s3-user"], "MINIO_ROOT_PASSWORD": seed["s3-password"]},
	)
	assertSecretValues(
		t,
		r,
		release+"-identity-seed",
		map[string]string{
			"OIDC_CLIENT_SECRET": seed["oidc-secret"],
			"CASDOOR_SECRET":     seed["casdoor-secret"],
			"BOOTSTRAP_PASSWORD": seed["bootstrap-password"],
		},
	)
	assertSecretValues(
		t,
		r,
		release+"-tenant-env",
		map[string]string{
			"DATABASE_URL": "postgres://forge:" + seed["tenant-password"] + "@" + release + "-tenant-postgres:5432/forge?sslmode=disable",
		},
	)
	assertSecretValues(
		t,
		r,
		release+"-artifact-env",
		map[string]string{
			"ARTIFACT_DATABASE_URL": "postgres://artifact_app:" + seed["artifact-password"] + "@" + release + "-artifact-postgres:5432/artifact?sslmode=disable",
			"STORAGE_DATABASE_URL":  "postgres://storage_app:" + seed["storage-password"] + "@" + release + "-artifact-postgres:5432/storage?sslmode=disable",
		},
	)
	assertSecretValues(
		t,
		r,
		release+"-tenant-env",
		map[string]string{
			"PUBLIC_URL":            "http://127.0.0.1:8483",
			"CASDOOR_URL":           "http://casdoor.tenant-lab.svc.cluster.local:8000",
			"OIDC_ISSUER":           "https://identity.tenant-lab.svc.cluster.local:8080",
			"GITLAB_URL":            "http://gitlab-webservice-default.tenant-lab.svc.cluster.local:8181",
			"GITLAB_PUBLIC_URL":     "http://127.0.0.1:18080",
			"GITLAB_OIDC_CLIENT_ID": "tenant-lab-gitlab",
		},
	)
}

func assertTLS(t *testing.T, r rendered, release, namespace string) {
	t.Helper()
	data := secretData(t, r, release+"-tls")
	for _, component := range []string{"artifact", "tenant"} {
		certPEM := []byte(data[component+".crt"])
		keyPEM := []byte(data[component+".key"])
		certBlock, _ := pem.Decode(certPEM)
		keyBlock, _ := pem.Decode(keyPEM)
		if certBlock == nil || keyBlock == nil {
			t.Errorf("%s TLS certificate or key has no PEM block", component)
			continue
		}
		cert, err := x509.ParseCertificate(certBlock.Bytes)
		if err != nil {
			t.Errorf("%s TLS certificate is invalid", component)
			continue
		}
		var privateKey interface{ Public() crypto.PublicKey }
		if parsed, parseErr := x509.ParsePKCS1PrivateKey(keyBlock.Bytes); parseErr == nil {
			privateKey = parsed
		} else {
			parsed, parseErr := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
			if parseErr != nil {
				t.Errorf("%s TLS private key is invalid", component)
				continue
			}
			var ok bool
			privateKey, ok = parsed.(interface{ Public() crypto.PublicKey })
			if !ok {
				t.Errorf("%s TLS private key has unsupported type", component)
				continue
			}
		}
		publicKey, err := x509.MarshalPKIXPublicKey(privateKey.Public())
		if err != nil || !bytes.Equal(publicKey, cert.RawSubjectPublicKeyInfo) {
			t.Errorf("%s TLS certificate and private key do not match", component)
			continue
		}
		if cert.Subject.CommonName != release+"-"+component {
			t.Errorf("%s certificate common name does not match release", component)
		}
		for _, host := range []string{release + "-" + component, release + "-" + component + "." + namespace, release + "-" + component + "." + namespace + ".svc", release + "-" + component + "." + namespace + ".svc.cluster.local"} {
			if err := cert.VerifyHostname(host); err != nil {
				t.Errorf("%s certificate SAN does not cover expected service host", component)
			}
		}
	}
}

func assertWorkloadContract(t *testing.T, r rendered, release string) {
	t.Helper()
	artifact := r.resources["Deployment/"+release+"-artifact"]
	if artifact == nil {
		t.Fatalf("missing artifact Deployment")
	}
	pod := podSpec(artifact)
	for _, c := range sliceMaps(pod["containers"]) {
		for _, mount := range sliceMaps(c["volumeMounts"]) {
			if strings.Contains(stringValue(mount["mountPath"]), "bootstrap-admin") {
				t.Errorf("business workload mounts bootstrap-admin credentials")
			}
		}
	}
	containers := sliceMaps(pod["initContainers"])
	initNames := make(map[string]bool)
	for _, c := range containers {
		initNames[stringValue(c["name"])] = true
	}
	if !initNames["migrate"] || !initNames["storage-iam"] {
		t.Errorf("artifact init path must include migrate and storage-iam")
	}
	foundRuntime := false
	for _, cm := range r.byKind["ConfigMap"] {
		if stringValue(mapValue(cm["metadata"])["name"]) != release+"-artifact-runtime" {
			continue
		}
		foundRuntime = true
		config := parseJSON(t, stringValue(mapValue(cm["data"])["config.json"]))
		for _, path := range [][]string{{"DB", "AutoMigrate"}, {"StorageDB", "AutoMigrate"}} {
			if valueAt(config, path) != false {
				t.Errorf("artifact config %s AutoMigrate is not false", strings.Join(path, "."))
			}
		}
	}
	if !foundRuntime {
		t.Errorf("artifact runtime ConfigMap is missing")
	}
	foundWebConfig := false
	for _, cm := range r.byKind["ConfigMap"] {
		name := stringValue(mapValue(cm["metadata"])["name"])
		if name != release+"-web-nginx" {
			continue
		}
		foundWebConfig = true
		nginx := stringValue(mapValue(cm["data"])["default.conf"])
		if nginx == "" {
			t.Fatal("web nginx default.conf is missing or empty")
		}
		for _, name := range []string{"Coding", "Agent"} {
			if !strings.Contains(nginx, `return 404 '{"message":"`+name+` is disabled"}';`) {
				t.Errorf("disabled %s route does not reject requests", name)
			}
		}
		for _, line := range strings.Split(nginx, "\n") {
			if strings.Contains(line, "proxy_pass") &&
				(strings.Contains(line, "coding") || strings.Contains(line, "agent")) {
				t.Errorf("disabled coding/agent upstream appears in web nginx config")
			}
		}
	}
	if !foundWebConfig {
		t.Errorf("web nginx ConfigMap is missing")
	}
	for _, svc := range r.byKind["Service"] {
		name := stringValue(mapValue(svc["metadata"])["name"])
		if strings.Contains(name, "coding") || strings.Contains(name, "agent") {
			t.Errorf("disabled coding/agent Service %s is rendered", name)
		}
	}
	for _, workload := range append(append([]resource{}, r.byKind["Deployment"]...), r.byKind["StatefulSet"]...) {
		name := stringValue(mapValue(workload["metadata"])["name"])
		if strings.Contains(name, "coding") || strings.Contains(name, "agent") {
			t.Errorf("disabled coding/agent workload %s is rendered", name)
		}
	}
}

func assertRoleScope(t *testing.T, r rendered, release string) {
	t.Helper()
	role := r.resources["Role/"+release+"-external-bootstrap"]
	if role == nil {
		t.Fatalf("missing external-bootstrap Role")
	}
	roleRules := sliceMaps(role["rules"])
	if len(roleRules) != 1 {
		t.Fatalf("external-bootstrap Role has %d rules; want exactly one", len(roleRules))
	}
	rule := roleRules[0]
	if !equalStrings(stringSlice(rule["resources"]), []string{"secrets"}) ||
		!equalStrings(stringSlice(rule["resourceNames"]), []string{release + "-external-credentials"}) ||
		!equalStrings(stringSlice(rule["verbs"]), []string{"get", "patch"}) {
		t.Errorf("external-bootstrap Role is not restricted to its output Secret get/patch")
	}
}

func assertBootstrapContract(t *testing.T, r rendered, release, namespace string) {
	t.Helper()
	configMap := r.resources["ConfigMap/"+release+"-external-bootstrap"]
	if configMap == nil {
		t.Fatalf("missing external bootstrap ConfigMap")
	}
	config := parseJSON(t, stringValue(mapValue(configMap["data"])["config.json"]))
	for _, key := range []string{"CasdoorURL", "CasdoorOrganization", "CasdoorCertificate", "OIDCApplication", "ManagementApplication", "PublicURL", "BootstrapUsername", "BootstrapEmail", "GitLabURL", "GitLabTokenName", "GitLabTokenExpiresAt", "Namespace", "OutputSecret", "AdminDirectory", "SeedDirectory", "CAFile", "TimeoutSeconds"} {
		if _, ok := config[key]; !ok {
			t.Errorf("external bootstrap config is missing %s", key)
		}
	}
	if len(config) != 17 {
		t.Errorf("external bootstrap config has %d keys; the tenant CLI rejects unknown fields", len(config))
	}
	seed := secretData(t, r, release+"-identity-seed")
	if config["OIDCApplication"] != seed["OIDC_CLIENT_ID"] ||
		config["ManagementApplication"] != seed["CASDOOR_CLIENT_ID"] {
		t.Errorf("Casdoor application names do not match the identity seed client IDs")
	}
	if config["Namespace"] != namespace || config["OutputSecret"] != release+"-external-credentials" {
		t.Errorf("external bootstrap writes to an unexpected namespace or Secret")
	}
	if config["AdminDirectory"] != "/bootstrap-admin" || config["SeedDirectory"] != "/identity-seed" ||
		config["CAFile"] != "/external-ca/ca.crt" {
		t.Errorf("external bootstrap directories do not match the Job mounts")
	}
	job := r.resources["Job/"+release+"-external-bootstrap-1"]
	if job == nil {
		t.Fatalf("missing external bootstrap Job")
	}
	containers := sliceMaps(podSpec(job)["containers"])
	if len(containers) != 1 {
		t.Fatalf("external bootstrap Job has %d containers; want one", len(containers))
	}
	if !equalStrings(stringSlice(containers[0]["command"]), []string{"/forge"}) ||
		!equalStrings(
			stringSlice(containers[0]["args"]),
			[]string{"external-bootstrap", "--config", "/bootstrap/config.json"},
		) {
		t.Errorf("external bootstrap Job does not call the tenant CLI contract")
	}
	mounts := map[string]string{}
	for _, mount := range sliceMaps(containers[0]["volumeMounts"]) {
		mounts[stringValue(mount["name"])] = stringValue(mount["mountPath"])
	}
	if mounts["admin"] != config["AdminDirectory"] || mounts["seed"] != config["SeedDirectory"] ||
		mounts["ca"] != "/external-ca" ||
		mounts["config"] != "/bootstrap" {
		t.Errorf("external bootstrap mounts do not match the config directories")
	}
	for _, volume := range sliceMaps(podSpec(job)["volumes"]) {
		switch stringValue(volume["name"]) {
		case "admin":
			if stringValue(mapValue(volume["secret"])["secretName"]) != "fixture-admin" {
				t.Errorf("admin volume does not mount the supplied bootstrap Secret")
			}
		case "seed":
			if stringValue(mapValue(volume["secret"])["secretName"]) != release+"-identity-seed" {
				t.Errorf("seed volume does not mount the generated identity seed")
			}
		case "ca":
			if stringValue(mapValue(volume["secret"])["secretName"]) != release+"-external-ca" {
				t.Errorf("CA volume does not mount the external CA Secret")
			}
		}
	}
}

func secretData(t *testing.T, r rendered, name string) map[string]string {
	t.Helper()
	doc := r.resources["Secret/"+name]
	if doc == nil {
		t.Fatalf("missing Secret %s", name)
	}
	result := make(map[string]string)
	for key, value := range mapValue(doc["data"]) {
		encoded, ok := value.(string)
		if !ok {
			t.Errorf("Secret %s data key %s is not a string", name, key)
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			t.Errorf("Secret %s data key %s is not base64", name, key)
			continue
		}
		result[key] = string(decoded)
	}
	for key, value := range mapValue(doc["stringData"]) {
		if text, ok := value.(string); ok {
			result[key] = text
		}
	}
	return result
}

func assertSecretValues(t *testing.T, r rendered, name string, expected map[string]string) {
	t.Helper()
	actual := secretData(t, r, name)
	for key, value := range expected {
		if actual[key] != value {
			t.Errorf("Secret %s key %s does not match its seed value", name, key)
		}
	}
}

func podSpec(doc resource) map[string]any {
	spec := mapValue(doc["spec"])
	if stringValue(doc["kind"]) == "Job" {
		return mapValue(mapValue(mapValue(spec["template"])["spec"]))
	}
	return mapValue(mapValue(mapValue(spec["template"])["spec"]))
}

func parseJSON(t *testing.T, value string) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal([]byte(value), &result); err != nil {
		t.Fatalf("decode rendered application config: %v", err)
	}
	return result
}

func valueAt(value map[string]any, path []string) any {
	var current any = value
	for _, key := range path {
		current = mapValue(current)[key]
	}
	return current
}

func nameSet(docs []resource) map[string]bool {
	result := make(map[string]bool, len(docs))
	for _, doc := range docs {
		result[stringValue(mapValue(doc["metadata"])["name"])] = true
	}
	return result
}

func requireKnown(t *testing.T, where, name string, known map[string]bool) {
	t.Helper()
	if name == "" || !known[name] {
		t.Errorf("%s refers to unresolved resource %q", where, name)
	}
}

func walkMaps(value any, visit func(map[string]any)) {
	switch v := value.(type) {
	case map[string]any:
		visit(v)
		for _, child := range v {
			walkMaps(child, visit)
		}
	case []any:
		for _, child := range v {
			walkMaps(child, visit)
		}
	}
}

func containsLabels(labels, selector map[string]any) bool {
	for key, value := range selector {
		if labels[key] != value {
			return false
		}
	}
	return true
}

func mapValue(value any) map[string]any {
	if result, ok := value.(map[string]any); ok {
		return result
	}
	return map[string]any{}
}

func sliceMaps(value any) []map[string]any {
	items, _ := value.([]any)
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if m, ok := item.(map[string]any); ok {
			result = append(result, m)
		}
	}
	return result
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}

func stringSlice(value any) []string {
	items, _ := value.([]any)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func objectKey(doc resource) string {
	return stringValue(doc["kind"]) + "/" + stringValue(mapValue(doc["metadata"])["name"])
}
