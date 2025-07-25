package kustomize

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/Masterminds/semver/v3"
	"sigs.k8s.io/yaml"

	"github.com/argoproj/argo-cd/v3/util/io"

	"github.com/argoproj/gitops-engine/pkg/utils/kube"
	log "github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	certutil "github.com/argoproj/argo-cd/v3/util/cert"
	executil "github.com/argoproj/argo-cd/v3/util/exec"
	"github.com/argoproj/argo-cd/v3/util/git"
	"github.com/argoproj/argo-cd/v3/util/proxy"
)

// Image represents a Docker image in the format NAME[:TAG].
type Image = string

type BuildOpts struct {
	KubeVersion string
	APIVersions []string
}

// Kustomize provides wrapper functionality around the `kustomize` command.
type Kustomize interface {
	// Build returns a list of unstructured objects from a `kustomize build` command and extract supported parameters
	Build(opts *v1alpha1.ApplicationSourceKustomize, kustomizeOptions *v1alpha1.KustomizeOptions, envVars *v1alpha1.Env, buildOpts *BuildOpts) ([]*unstructured.Unstructured, []Image, []string, error)
}

// NewKustomizeApp create a new wrapper to run commands on the `kustomize` command-line tool.
func NewKustomizeApp(repoRoot string, path string, creds git.Creds, fromRepo string, binaryPath string, proxy string, noProxy string) Kustomize {
	return &kustomize{
		repoRoot:   repoRoot,
		path:       path,
		creds:      creds,
		repo:       fromRepo,
		binaryPath: binaryPath,
		proxy:      proxy,
		noProxy:    noProxy,
	}
}

type kustomize struct {
	// path to the Git repository root
	repoRoot string
	// path inside the checked out tree
	path string
	// creds structure
	creds git.Creds
	// the Git repository URL where we checked out
	repo string
	// optional kustomize binary path
	binaryPath string
	// HTTP/HTTPS proxy used to access repository
	proxy string
	// NoProxy specifies a list of targets where the proxy isn't used, applies only in cases where the proxy is applied
	noProxy string
}

var KustomizationNames = []string{"kustomization.yaml", "kustomization.yml", "Kustomization"}

// IsKustomization checks if the given file name matches any known kustomization file names.
func IsKustomization(path string) bool {
	for _, kustomization := range KustomizationNames {
		if path == kustomization {
			return true
		}
	}
	return false
}

// findKustomizeFile looks for any known kustomization file in the path
func findKustomizeFile(dir string) string {
	for _, file := range KustomizationNames {
		path := filepath.Join(dir, file)
		if _, err := os.Stat(path); err == nil {
			return file
		}
	}

	return ""
}

func (k *kustomize) getBinaryPath() string {
	if k.binaryPath != "" {
		return k.binaryPath
	}
	return "kustomize"
}

// kustomize v3.8.5 patch release introduced a breaking change in "edit add <label/annotation>" commands:
// https://github.com/kubernetes-sigs/kustomize/commit/b214fa7d5aa51d7c2ae306ec15115bf1c044fed8#diff-0328c59bcd29799e365ff0647653b886f17c8853df008cd54e7981db882c1b36
func mapToEditAddArgs(val map[string]string) []string {
	var args []string
	if getSemverSafe(&kustomize{}).LessThan(semver.MustParse("v3.8.5")) {
		arg := ""
		for labelName, labelValue := range val {
			if arg != "" {
				arg += ","
			}
			arg += fmt.Sprintf("%s:%s", labelName, labelValue)
		}
		args = append(args, arg)
	} else {
		for labelName, labelValue := range val {
			args = append(args, fmt.Sprintf("%s:%s", labelName, labelValue))
		}
	}
	return args
}

func (k *kustomize) Build(opts *v1alpha1.ApplicationSourceKustomize, kustomizeOptions *v1alpha1.KustomizeOptions, envVars *v1alpha1.Env, buildOpts *BuildOpts) ([]*unstructured.Unstructured, []Image, []string, error) {
	// commands stores all the commands that were run as part of this build.
	var commands []string

	env := os.Environ()
	if envVars != nil {
		env = append(env, envVars.Environ()...)
	}

	closer, environ, err := k.creds.Environ()
	if err != nil {
		return nil, nil, nil, err
	}
	defer func() { _ = closer.Close() }()

	// If we were passed a HTTPS URL, make sure that we also check whether there
	// is a custom CA bundle configured for connecting to the server.
	if k.repo != "" && git.IsHTTPSURL(k.repo) {
		parsedURL, err := url.Parse(k.repo)
		if err != nil {
			log.Warnf("Could not parse URL %s: %v", k.repo, err)
		} else {
			caPath, err := certutil.GetCertBundlePathForRepository(parsedURL.Host)
			switch {
			case err != nil:
				// Some error while getting CA bundle
				log.Warnf("Could not get CA bundle path for %s: %v", parsedURL.Host, err)
			case caPath == "":
				// No cert configured
				log.Debugf("No caCert found for repo %s", parsedURL.Host)
			default:
				// Make Git use CA bundle
				environ = append(environ, "GIT_SSL_CAINFO="+caPath)
			}
		}
	}

	env = append(env, environ...)

	if opts != nil {
		if opts.NamePrefix != "" {
			cmd := exec.Command(k.getBinaryPath(), "edit", "set", "nameprefix", "--", opts.NamePrefix)
			cmd.Dir = k.path
			commands = append(commands, executil.GetCommandArgsToLog(cmd))
			_, err := executil.Run(cmd)
			if err != nil {
				return nil, nil, nil, err
			}
		}
		if opts.NameSuffix != "" {
			cmd := exec.Command(k.getBinaryPath(), "edit", "set", "namesuffix", "--", opts.NameSuffix)
			cmd.Dir = k.path
			commands = append(commands, executil.GetCommandArgsToLog(cmd))
			_, err := executil.Run(cmd)
			if err != nil {
				return nil, nil, nil, err
			}
		}
		if len(opts.Images) > 0 {
			// set image postgres=eu.gcr.io/my-project/postgres:latest my-app=my-registry/my-app@sha256:24a0c4b4a4c0eb97a1aabb8e29f18e917d05abfe1b7a7c07857230879ce7d3d3
			// set image node:8.15.0 mysql=mariadb alpine@sha256:24a0c4b4a4c0eb97a1aabb8e29f18e917d05abfe1b7a7c07857230879ce7d3d3
			args := []string{"edit", "set", "image"}
			for _, image := range opts.Images {
				// this allows using ${ARGOCD_APP_REVISION}
				envSubstitutedImage := envVars.Envsubst(string(image))
				args = append(args, envSubstitutedImage)
			}
			cmd := exec.Command(k.getBinaryPath(), args...)
			cmd.Dir = k.path
			commands = append(commands, executil.GetCommandArgsToLog(cmd))
			_, err := executil.Run(cmd)
			if err != nil {
				return nil, nil, nil, err
			}
		}

		if len(opts.Replicas) > 0 {
			// set replicas my-development=2 my-statefulset=4
			args := []string{"edit", "set", "replicas"}
			for _, replica := range opts.Replicas {
				count, err := replica.GetIntCount()
				if err != nil {
					return nil, nil, nil, err
				}
				arg := fmt.Sprintf("%s=%d", replica.Name, count)
				args = append(args, arg)
			}

			cmd := exec.Command(k.getBinaryPath(), args...)
			cmd.Dir = k.path
			commands = append(commands, executil.GetCommandArgsToLog(cmd))
			_, err := executil.Run(cmd)
			if err != nil {
				return nil, nil, nil, err
			}
		}

		if len(opts.CommonLabels) > 0 {
			//  edit add label foo:bar
			args := []string{"edit", "add", "label"}
			if opts.ForceCommonLabels {
				args = append(args, "--force")
			}
			if opts.LabelWithoutSelector {
				args = append(args, "--without-selector")
			}
			if opts.LabelIncludeTemplates {
				args = append(args, "--include-templates")
			}
			commonLabels := map[string]string{}
			for name, value := range opts.CommonLabels {
				commonLabels[name] = envVars.Envsubst(value)
			}
			cmd := exec.Command(k.getBinaryPath(), append(args, mapToEditAddArgs(commonLabels)...)...)
			cmd.Dir = k.path
			commands = append(commands, executil.GetCommandArgsToLog(cmd))
			_, err := executil.Run(cmd)
			if err != nil {
				return nil, nil, nil, err
			}
		}

		if len(opts.CommonAnnotations) > 0 {
			//  edit add annotation foo:bar
			args := []string{"edit", "add", "annotation"}
			if opts.ForceCommonAnnotations {
				args = append(args, "--force")
			}
			var commonAnnotations map[string]string
			if opts.CommonAnnotationsEnvsubst {
				commonAnnotations = map[string]string{}
				for name, value := range opts.CommonAnnotations {
					commonAnnotations[name] = envVars.Envsubst(value)
				}
			} else {
				commonAnnotations = opts.CommonAnnotations
			}
			cmd := exec.Command(k.getBinaryPath(), append(args, mapToEditAddArgs(commonAnnotations)...)...)
			cmd.Dir = k.path
			commands = append(commands, executil.GetCommandArgsToLog(cmd))
			_, err := executil.Run(cmd)
			if err != nil {
				return nil, nil, nil, err
			}
		}

		if opts.Namespace != "" {
			cmd := exec.Command(k.getBinaryPath(), "edit", "set", "namespace", "--", opts.Namespace)
			cmd.Dir = k.path
			commands = append(commands, executil.GetCommandArgsToLog(cmd))
			_, err := executil.Run(cmd)
			if err != nil {
				return nil, nil, nil, err
			}
		}

		if len(opts.Patches) > 0 {
			kustFile := findKustomizeFile(k.path)
			// If the kustomization file is not found, return early.
			// There is no point reading the kustomization path if it doesn't exist.
			if kustFile == "" {
				return nil, nil, nil, errors.New("kustomization file not found in the path")
			}
			kustomizationPath := filepath.Join(k.path, kustFile)
			b, err := os.ReadFile(kustomizationPath)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("failed to load kustomization.yaml: %w", err)
			}
			var kustomization any
			err = yaml.Unmarshal(b, &kustomization)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("failed to unmarshal kustomization.yaml: %w", err)
			}
			kMap, ok := kustomization.(map[string]any)
			if !ok {
				return nil, nil, nil, fmt.Errorf("expected kustomization.yaml to be type map[string]any, but got %T", kMap)
			}
			patches, ok := kMap["patches"]
			if ok {
				// The kustomization.yaml already had a patches field, so we need to append to it.
				patchesList, ok := patches.([]any)
				if !ok {
					return nil, nil, nil, fmt.Errorf("expected 'patches' field in kustomization.yaml to be []any, but got %T", patches)
				}
				// Since the patches from the Application manifest are typed, we need to convert them to a type which
				// can be appended to the existing list.
				untypedPatches := make([]any, len(opts.Patches))
				for i := range opts.Patches {
					untypedPatches[i] = opts.Patches[i]
				}
				patchesList = append(patchesList, untypedPatches...)
				// Update the kustomization.yaml with the appended patches list.
				kMap["patches"] = patchesList
			} else {
				kMap["patches"] = opts.Patches
			}
			updatedKustomization, err := yaml.Marshal(kMap)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("failed to marshal kustomization.yaml after adding patches: %w", err)
			}
			kustomizationFileInfo, err := os.Stat(kustomizationPath)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("failed to stat kustomization.yaml: %w", err)
			}
			err = os.WriteFile(kustomizationPath, updatedKustomization, kustomizationFileInfo.Mode())
			if err != nil {
				return nil, nil, nil, fmt.Errorf("failed to write kustomization.yaml with updated 'patches' field: %w", err)
			}
			commands = append(commands, "# kustomization.yaml updated with patches. There is no `kustomize edit` command for adding patches. In order to generate the manifests in your local environment, you will need to copy the patches into kustomization.yaml manually.")
		}

		if len(opts.Components) > 0 {
			// components only supported in kustomize >= v3.7.0
			// https://github.com/kubernetes-sigs/kustomize/blob/master/examples/components.md
			if getSemverSafe(k).LessThan(semver.MustParse("v3.7.0")) {
				return nil, nil, nil, errors.New("kustomize components require kustomize v3.7.0 and above")
			}

			// add components
			foundComponents := opts.Components
			if opts.IgnoreMissingComponents {
				foundComponents = make([]string, 0)
				root, err := os.OpenRoot(k.repoRoot)
				defer io.Close(root)
				if err != nil {
					return nil, nil, nil, fmt.Errorf("failed to open the repo folder: %w", err)
				}

				for _, c := range opts.Components {
					resolvedPath, err := filepath.Rel(k.repoRoot, filepath.Join(k.path, c))
					if err != nil {
						return nil, nil, nil, fmt.Errorf("kustomize components path failed: %w", err)
					}
					_, err = root.Stat(resolvedPath)
					if err != nil {
						log.Debugf("%s component directory does not exist", resolvedPath)
						continue
					}
					foundComponents = append(foundComponents, c)
				}
			}
			args := []string{"edit", "add", "component"}
			args = append(args, foundComponents...)
			cmd := exec.Command(k.getBinaryPath(), args...)
			cmd.Dir = k.path
			cmd.Env = env
			commands = append(commands, executil.GetCommandArgsToLog(cmd))
			_, err := executil.Run(cmd)
			if err != nil {
				return nil, nil, nil, err
			}
		}
	}

	var cmd *exec.Cmd
	if kustomizeOptions != nil && kustomizeOptions.BuildOptions != "" {
		params := parseKustomizeBuildOptions(k, kustomizeOptions.BuildOptions, buildOpts)
		cmd = exec.Command(k.getBinaryPath(), params...)
	} else {
		cmd = exec.Command(k.getBinaryPath(), "build", k.path)
	}
	cmd.Env = env
	cmd.Env = proxy.UpsertEnv(cmd, k.proxy, k.noProxy)
	cmd.Dir = k.repoRoot
	commands = append(commands, executil.GetCommandArgsToLog(cmd))
	out, err := executil.Run(cmd)
	if err != nil {
		return nil, nil, nil, err
	}

	objs, err := kube.SplitYAML([]byte(out))
	if err != nil {
		return nil, nil, nil, err
	}

	redactedCommands := make([]string, len(commands))
	for i, c := range commands {
		redactedCommands[i] = strings.ReplaceAll(c, k.repoRoot, ".")
	}

	return objs, getImageParameters(objs), redactedCommands, nil
}

func parseKustomizeBuildOptions(k *kustomize, buildOptions string, buildOpts *BuildOpts) []string {
	buildOptsParams := append([]string{"build", k.path}, strings.Fields(buildOptions)...)

	if buildOpts != nil && !getSemverSafe(k).LessThan(semver.MustParse("v5.3.0")) && isHelmEnabled(buildOptions) {
		if buildOpts.KubeVersion != "" {
			buildOptsParams = append(buildOptsParams, "--helm-kube-version", buildOpts.KubeVersion)
		}
		for _, v := range buildOpts.APIVersions {
			buildOptsParams = append(buildOptsParams, "--helm-api-versions", v)
		}
	}

	return buildOptsParams
}

func isHelmEnabled(buildOptions string) bool {
	return strings.Contains(buildOptions, "--enable-helm")
}

// semver/v3 doesn't export the regexp anymore, so shamelessly copied it over to
// here.
// https://github.com/Masterminds/semver/blob/49c09bfed6adcffa16482ddc5e5588cffff9883a/version.go#L42
const semVerRegex string = `v?([0-9]+)(\.[0-9]+)?(\.[0-9]+)?` +
	`(-([0-9A-Za-z\-]+(\.[0-9A-Za-z\-]+)*))?` +
	`(\+([0-9A-Za-z\-]+(\.[0-9A-Za-z\-]+)*))?`

var (
	unknownVersion = semver.MustParse("v99.99.99")
	semverRegex    = regexp.MustCompile(semVerRegex)
	semVer         *semver.Version
	semVerLock     sync.Mutex
)

// getSemver returns parsed kustomize version
func getSemver(k *kustomize) (*semver.Version, error) {
	verStr, err := versionWithBinaryPath(k)
	if err != nil {
		return nil, err
	}

	semverMatches := semverRegex.FindStringSubmatch(verStr)
	if len(semverMatches) == 0 {
		return nil, fmt.Errorf("expected string that includes semver formatted version but got: '%s'", verStr)
	}

	return semver.NewVersion(semverMatches[0])
}

// getSemverSafe returns parsed kustomize version;
// if version cannot be parsed assumes that "kustomize version" output format changed again
// and fallback to latest ( v99.99.99 )
func getSemverSafe(k *kustomize) *semver.Version {
	if semVer == nil {
		semVerLock.Lock()
		defer semVerLock.Unlock()

		if ver, err := getSemver(k); err != nil {
			semVer = unknownVersion
			log.Warnf("Failed to parse kustomize version: %v", err)
		} else {
			semVer = ver
		}
	}
	return semVer
}

func Version() (string, error) {
	return versionWithBinaryPath(&kustomize{})
}

func versionWithBinaryPath(k *kustomize) (string, error) {
	executable := k.getBinaryPath()
	cmd := exec.Command(executable, "version", "--short")
	// example version output:
	// short: "{kustomize/v3.8.1  2020-07-16T00:58:46Z  }"
	version, err := executil.Run(cmd)
	if err != nil {
		return "", fmt.Errorf("could not get kustomize version: %w", err)
	}
	version = strings.TrimSpace(version)
	// trim the curly braces
	version = strings.TrimPrefix(version, "{")
	version = strings.TrimSuffix(version, "}")
	version = strings.TrimSpace(version)

	// remove double space in middle
	version = strings.ReplaceAll(version, "  ", " ")

	// remove extra 'kustomize/' before version
	version = strings.TrimPrefix(version, "kustomize/")
	return version, nil
}

func getImageParameters(objs []*unstructured.Unstructured) []Image {
	var images []Image
	for _, obj := range objs {
		images = append(images, getImages(obj.Object)...)
	}
	sort.Strings(images)
	return images
}

func getImages(object map[string]any) []Image {
	var images []Image
	for k, v := range object {
		switch v := v.(type) {
		case []any:
			if k == "containers" || k == "initContainers" {
				for _, obj := range v {
					if mapObj, isMapObj := obj.(map[string]any); isMapObj {
						if image, hasImage := mapObj["image"]; hasImage {
							images = append(images, fmt.Sprintf("%s", image))
						}
					}
				}
			} else {
				for i := range v {
					if mapObj, isMapObj := v[i].(map[string]any); isMapObj {
						images = append(images, getImages(mapObj)...)
					}
				}
			}
		case map[string]any:
			images = append(images, getImages(v)...)
		}
	}
	return images
}
