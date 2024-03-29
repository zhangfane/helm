/*
Copyright The Helm Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package action

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pkg/errors"
	yamlv2 "gopkg.in/yaml.v2"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/huolunl/helm/v3/internal/experimental/registry"
	"github.com/huolunl/helm/v3/pkg/chart"
	"github.com/huolunl/helm/v3/pkg/chartutil"
	"github.com/huolunl/helm/v3/pkg/engine"
	"github.com/huolunl/helm/v3/pkg/kube"
	"github.com/huolunl/helm/v3/pkg/postrender"
	"github.com/huolunl/helm/v3/pkg/release"
	"github.com/huolunl/helm/v3/pkg/releaseutil"
	"github.com/huolunl/helm/v3/pkg/storage"
	"github.com/huolunl/helm/v3/pkg/storage/driver"
	"github.com/huolunl/helm/v3/pkg/time"
)

// Timestamper is a function capable of producing a timestamp.Timestamper.
//
// By default, this is a time.Time function from the Helm time package. This can
// be overridden for testing though, so that timestamps are predictable.
var Timestamper = time.Now

var (
	// errMissingChart indicates that a chart was not provided.
	errMissingChart = errors.New("no chart provided")
	// errMissingRelease indicates that a release (name) was not provided.
	errMissingRelease = errors.New("no release provided")
	// errInvalidRevision indicates that an invalid release revision number was provided.
	errInvalidRevision = errors.New("invalid release revision")
	// errPending indicates that another instance of Helm is already applying an operation on a release.
	errPending = errors.New("another operation (install/upgrade/rollback) is in progress")
)

// ValidName is a regular expression for resource names.
//
// DEPRECATED: This will be removed in Helm 4, and is no longer used here. See
// pkg/lint/rules.validateMetadataNameFunc for the replacement.
//
// According to the Kubernetes help text, the regular expression it uses is:
//
//	[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*
//
// This follows the above regular expression (but requires a full string match, not partial).
//
// The Kubernetes documentation is here, though it is not entirely correct:
// https://kubernetes.io/docs/concepts/overview/working-with-objects/names/#names
var ValidName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

// Configuration injects the dependencies that all actions share.
type Configuration struct {
	// RESTClientGetter is an interface that loads Kubernetes clients.
	RESTClientGetter RESTClientGetter

	// Releases stores records of releases.
	Releases *storage.Storage

	// KubeClient is a Kubernetes API client.
	KubeClient kube.Interface

	// RegistryClient is a client for working with registries
	RegistryClient *registry.Client

	// Capabilities describes the capabilities of the Kubernetes cluster.
	Capabilities *chartutil.Capabilities

	Log func(string, ...interface{})
}

// renderResources renders the templates in a chart
//
// TODO: This function is badly in need of a refactor.
// TODO: As part of the refactor the duplicate code in cmd/helm/template.go should be removed
//       This code has to do with writing files to disk.
func (c *Configuration) renderResources(ch *chart.Chart, values chartutil.Values, releaseName, outputDir string, subNotes, useReleaseName, includeCrds bool, pr postrender.PostRenderer, dryRun bool) ([]*release.Hook, *bytes.Buffer, string, error) {
	hs := []*release.Hook{}
	b := bytes.NewBuffer(nil)

	caps, err := c.getCapabilities()
	if err != nil {
		return hs, b, "", err
	}

	if ch.Metadata.KubeVersion != "" {
		if !chartutil.IsCompatibleRange(ch.Metadata.KubeVersion, caps.KubeVersion.String()) {
			return hs, b, "", errors.Errorf("chart requires kubeVersion: %s which is incompatible with Kubernetes %s", ch.Metadata.KubeVersion, caps.KubeVersion.String())
		}
	}

	var files map[string]string
	var err2 error

	// A `helm template` or `helm install --dry-run` should not talk to the remote cluster.
	// It will break in interesting and exotic ways because other data (e.g. discovery)
	// is mocked. It is not up to the template author to decide when the user wants to
	// connect to the cluster. So when the user says to dry run, respect the user's
	// wishes and do not connect to the cluster.
	if !dryRun && c.RESTClientGetter != nil {
		rest, err := c.RESTClientGetter.ToRESTConfig()
		if err != nil {
			return hs, b, "", err
		}
		files, err2 = engine.RenderWithClient(ch, values, rest)
	} else {
		files, err2 = engine.Render(ch, values)
	}

	if err2 != nil {
		return hs, b, "", err2
	}

	// NOTES.txt gets rendered like all the other files, but because it's not a hook nor a resource,
	// pull it out of here into a separate file so that we can actually use the output of the rendered
	// text file. We have to spin through this map because the file contains path information, so we
	// look for terminating NOTES.txt. We also remove it from the files so that we don't have to skip
	// it in the sortHooks.
	var notesBuffer bytes.Buffer
	for k, v := range files {
		if strings.HasSuffix(k, notesFileSuffix) {
			if subNotes || (k == path.Join(ch.Name(), "templates", notesFileSuffix)) {
				// If buffer contains data, add newline before adding more
				if notesBuffer.Len() > 0 {
					notesBuffer.WriteString("\n")
				}
				notesBuffer.WriteString(v)
			}
			delete(files, k)
		}
	}
	notes := notesBuffer.String()

	// Sort hooks, manifests, and partials. Only hooks and manifests are returned,
	// as partials are not used after renderer.Render. Empty manifests are also
	// removed here.
	hs, manifests, err := releaseutil.SortManifests(files, caps.APIVersions, releaseutil.InstallOrder)
	if err != nil {
		// By catching parse errors here, we can prevent bogus releases from going
		// to Kubernetes.
		//
		// We return the files as a big blob of data to help the user debug parser
		// errors.
		for name, content := range files {
			if strings.TrimSpace(content) == "" {
				continue
			}
			fmt.Fprintf(b, "---\n# Source: %s\n%s\n", name, content)
		}
		return hs, b, "", err
	}

	//todo replace manifest
	const KEY = "nika.cai-inc.com"
	var v = releaseName
	for index, m := range manifests {
		if m.Head.Version == "apps/v1" && (m.Head.Kind == "Deployment" || m.Head.Kind == "ReplicaSet" || m.Head.Kind == "StatefulSet" || m.Head.Kind == "DaemonSet") {
			apiObj := new(K8sYamlStruct)
			err := yamlv2.Unmarshal([]byte(m.Content), apiObj)
			if err != nil {
				log.Println(err)
			}
			if apiObj.Metadata.Labels == nil {
				apiObj.Metadata.Labels = map[string]string{KEY: v}
			} else {
				apiObj.Metadata.Labels[KEY] = v
			}
			if apiObj.Spec.Selector.MatchLabels == nil {
				apiObj.Spec.Selector.MatchLabels = map[string]string{KEY: v}
			} else {
				apiObj.Spec.Selector.MatchLabels[KEY] = v
			}
			if apiObj.Spec.Template.Metadata.Labels == nil {
				apiObj.Spec.Template.Metadata.Labels = map[string]string{KEY: v}
			} else {
				apiObj.Spec.Template.Metadata.Labels[KEY] = v
			}
			var obj map[interface{}]interface{}
			err = yamlv2.Unmarshal([]byte(m.Content), &obj)
			if err != nil {
				log.Println(err)
			}
			for k, v := range obj {
				switch k.(type) {
				case string:
					if k == "metadata" {
						if v == nil {
							obj[k] = apiObj.Metadata
						} else {
							metadata := v.(map[interface{}]interface{})
							metadata["labels"] = apiObj.Metadata.Labels
							obj[k] = metadata
						}
					}
					if k == "spec" {
						if v == nil {
							obj[k] = apiObj.Spec
						} else {
							spec := v.(map[interface{}]interface{})
							var hasSelector bool
							var hasTemplate bool
							for k, v := range spec {
								switch k.(type) {
								case string:
									if k == "selector" {
										hasSelector = true
										if v == nil {
											spec[k] = apiObj.Spec.Selector
										} else {
											selector := v.(map[interface{}]interface{})
											selector["matchLabels"] = apiObj.Spec.Selector.MatchLabels
											spec[k] = selector
										}
									}
									if k == "template" {
										hasTemplate = true
										if v == nil {
											spec[k] = apiObj.Spec.Template
										} else {
											template := v.(map[interface{}]interface{})
											var hasMetadata bool
											for k, v := range template {
												switch k.(type) {
												case string:
													if k == "metadata" {
														hasMetadata = true
														if v == nil {
															template[k] = apiObj.Spec.Template.Metadata
														} else {
															metadata := v.(map[interface{}]interface{})
															metadata["labels"] = apiObj.Spec.Template.Metadata.Labels
															template[k] = metadata
														}
													}
												}
											}
											if !hasMetadata {
												template["metadata"] = apiObj.Spec.Template.Metadata
											}
											spec[k] = template
										}
									}

								}
							}
							if !hasSelector {
								spec["selector"] = apiObj.Spec.Selector
							}
							if !hasTemplate {
								spec["template"] = apiObj.Spec.Template
							}
							obj[k] = spec
						}
					}
				}
			}
			res, err := yamlv2.Marshal(obj)
			if err != nil {
				log.Println(err)
			}
			manifests[index].Content = string(res)
		}
		if (m.Head.Version == "v1" && (m.Head.Kind == "Pod" || m.Head.Kind == "Service" || m.Head.Kind == "PersistentVolumeClaim" || m.Head.Kind == "PersistentVolume" || m.Head.Kind == "ConfigMap" ||
			m.Head.Kind == "Secret" || m.Head.Kind == "ServiceAccount")) ||
			(m.Head.Version == "batch/v1" && (m.Head.Kind == "Job" || m.Head.Kind == "CronJob")) ||
			(m.Head.Version == "networking.k8s.io/v1" && (m.Head.Kind == "Ingress" || m.Head.Kind == "NetworkPolicy")) {
			apiObj := new(K8sYamlStruct)
			err := yamlv2.Unmarshal([]byte(m.Content), apiObj)
			if err != nil {
				log.Println(err)
			}
			if apiObj.Metadata.Labels == nil {
				apiObj.Metadata.Labels = map[string]string{KEY: v}
			} else {
				apiObj.Metadata.Labels[KEY] = v
			}
			var obj map[interface{}]interface{}
			err = yamlv2.Unmarshal([]byte(m.Content), &obj)
			if err != nil {
				log.Println(err)
			}
			for k, v := range obj {
				switch k.(type) {
				case string:
					if k == "metadata" {
						if v == nil {
							obj[k] = apiObj.Metadata
						} else {
							metadata := v.(map[interface{}]interface{})
							metadata["labels"] = apiObj.Metadata.Labels
							obj[k] = metadata
						}
					}
				}
			}
			content, err := yamlv2.Marshal(obj)
			if err != nil {
				log.Println(err)
			}
			manifests[index].Content = string(content)
		}
	}

	// Aggregate all valid manifests into one big doc.
	fileWritten := make(map[string]bool)

	if includeCrds {
		for _, crd := range ch.CRDObjects() {
			if outputDir == "" {
				fmt.Fprintf(b, "---\n# Source: %s\n%s\n", crd.Name, string(crd.File.Data[:]))
			} else {
				err = writeToFile(outputDir, crd.Filename, string(crd.File.Data[:]), fileWritten[crd.Name])
				if err != nil {
					return hs, b, "", err
				}
				fileWritten[crd.Name] = true
			}
		}
	}

	for _, m := range manifests {
		if outputDir == "" {
			fmt.Fprintf(b, "---\n# Source: %s\n%s\n", m.Name, m.Content)
		} else {
			newDir := outputDir
			if useReleaseName {
				newDir = filepath.Join(outputDir, releaseName)
			}
			// NOTE: We do not have to worry about the post-renderer because
			// output dir is only used by `helm template`. In the next major
			// release, we should move this logic to template only as it is not
			// used by install or upgrade
			err = writeToFile(newDir, m.Name, m.Content, fileWritten[m.Name])
			if err != nil {
				return hs, b, "", err
			}
			fileWritten[m.Name] = true
		}
	}

	if pr != nil {
		b, err = pr.Run(b)
		if err != nil {
			return hs, b, notes, errors.Wrap(err, "error while running post render on files")
		}
	}

	return hs, b, notes, nil
}
func (c *Configuration) renderResourcesForUpgrade(ch *chart.Chart, values chartutil.Values, releaseName, outputDir, name string, subNotes, useReleaseName, includeCrds bool, pr postrender.PostRenderer, dryRun bool) ([]*release.Hook, *bytes.Buffer, string, error) {
	hs := []*release.Hook{}
	b := bytes.NewBuffer(nil)

	caps, err := c.getCapabilities()
	if err != nil {
		return hs, b, "", err
	}

	if ch.Metadata.KubeVersion != "" {
		if !chartutil.IsCompatibleRange(ch.Metadata.KubeVersion, caps.KubeVersion.String()) {
			return hs, b, "", errors.Errorf("chart requires kubeVersion: %s which is incompatible with Kubernetes %s", ch.Metadata.KubeVersion, caps.KubeVersion.String())
		}
	}

	var files map[string]string
	var err2 error

	// A `helm template` or `helm install --dry-run` should not talk to the remote cluster.
	// It will break in interesting and exotic ways because other data (e.g. discovery)
	// is mocked. It is not up to the template author to decide when the user wants to
	// connect to the cluster. So when the user says to dry run, respect the user's
	// wishes and do not connect to the cluster.
	if !dryRun && c.RESTClientGetter != nil {
		rest, err := c.RESTClientGetter.ToRESTConfig()
		if err != nil {
			return hs, b, "", err
		}
		files, err2 = engine.RenderWithClient(ch, values, rest)
	} else {
		files, err2 = engine.Render(ch, values)
	}

	if err2 != nil {
		return hs, b, "", err2
	}

	// NOTES.txt gets rendered like all the other files, but because it's not a hook nor a resource,
	// pull it out of here into a separate file so that we can actually use the output of the rendered
	// text file. We have to spin through this map because the file contains path information, so we
	// look for terminating NOTES.txt. We also remove it from the files so that we don't have to skip
	// it in the sortHooks.
	var notesBuffer bytes.Buffer
	for k, v := range files {
		if strings.HasSuffix(k, notesFileSuffix) {
			if subNotes || (k == path.Join(ch.Name(), "templates", notesFileSuffix)) {
				// If buffer contains data, add newline before adding more
				if notesBuffer.Len() > 0 {
					notesBuffer.WriteString("\n")
				}
				notesBuffer.WriteString(v)
			}
			delete(files, k)
		}
	}
	notes := notesBuffer.String()

	// Sort hooks, manifests, and partials. Only hooks and manifests are returned,
	// as partials are not used after renderer.Render. Empty manifests are also
	// removed here.
	hs, manifests, err := releaseutil.SortManifests(files, caps.APIVersions, releaseutil.InstallOrder)
	if err != nil {
		// By catching parse errors here, we can prevent bogus releases from going
		// to Kubernetes.
		//
		// We return the files as a big blob of data to help the user debug parser
		// errors.
		for name, content := range files {
			if strings.TrimSpace(content) == "" {
				continue
			}
			fmt.Fprintf(b, "---\n# Source: %s\n%s\n", name, content)
		}
		return hs, b, "", err
	}

	//todo replace manifest
	const KEY = "nika.cai-inc.com"
	var v = name
	for index, m := range manifests {
		if m.Head.Version == "apps/v1" && (m.Head.Kind == "Deployment" || m.Head.Kind == "ReplicaSet" || m.Head.Kind == "StatefulSet" || m.Head.Kind == "DaemonSet") {
			apiObj := new(K8sYamlStruct)
			err := yamlv2.Unmarshal([]byte(m.Content), apiObj)
			if err != nil {
				log.Println(err)
			}
			if apiObj.Metadata.Labels == nil {
				apiObj.Metadata.Labels = map[string]string{KEY: v}
			} else {
				apiObj.Metadata.Labels[KEY] = v
			}
			if apiObj.Spec.Selector.MatchLabels == nil {
				apiObj.Spec.Selector.MatchLabels = map[string]string{KEY: v}
			} else {
				apiObj.Spec.Selector.MatchLabels[KEY] = v
			}
			if apiObj.Spec.Template.Metadata.Labels == nil {
				apiObj.Spec.Template.Metadata.Labels = map[string]string{KEY: v}
			} else {
				apiObj.Spec.Template.Metadata.Labels[KEY] = v
			}
			var obj map[interface{}]interface{}
			err = yamlv2.Unmarshal([]byte(m.Content), &obj)
			if err != nil {
				log.Println(err)
			}
			for k, v := range obj {
				switch k.(type) {
				case string:
					if k == "metadata" {
						if v == nil {
							obj[k] = apiObj.Metadata
						} else {
							metadata := v.(map[interface{}]interface{})
							metadata["labels"] = apiObj.Metadata.Labels
							obj[k] = metadata
						}
					}
					if k == "spec" {
						if v == nil {
							obj[k] = apiObj.Spec
						} else {
							spec := v.(map[interface{}]interface{})
							var hasSelector bool
							var hasTemplate bool
							for k, v := range spec {
								switch k.(type) {
								case string:
									if k == "selector" {
										hasSelector = true
										if v == nil {
											spec[k] = apiObj.Spec.Selector
										} else {
											selector := v.(map[interface{}]interface{})
											selector["matchLabels"] = apiObj.Spec.Selector.MatchLabels
											spec[k] = selector
										}
									}
									if k == "template" {
										hasTemplate = true
										if v == nil {
											spec[k] = apiObj.Spec.Template
										} else {
											template := v.(map[interface{}]interface{})
											var hasMetadata bool
											for k, v := range template {
												switch k.(type) {
												case string:
													if k == "metadata" {
														hasMetadata = true
														if v == nil {
															template[k] = apiObj.Spec.Template.Metadata
														} else {
															metadata := v.(map[interface{}]interface{})
															metadata["labels"] = apiObj.Spec.Template.Metadata.Labels
															template[k] = metadata
														}
													}
												}
											}
											if !hasMetadata {
												template["metadata"] = apiObj.Spec.Template.Metadata
											}
											spec[k] = template
										}
									}

								}
							}
							if !hasSelector {
								spec["selector"] = apiObj.Spec.Selector
							}
							if !hasTemplate {
								spec["template"] = apiObj.Spec.Template
							}
							obj[k] = spec
						}
					}
				}
			}
			res, err := yamlv2.Marshal(obj)
			if err != nil {
				log.Println(err)
			}
			manifests[index].Content = string(res)
		}
		if (m.Head.Version == "v1" && (m.Head.Kind == "Pod" || m.Head.Kind == "Service" || m.Head.Kind == "PersistentVolumeClaim" || m.Head.Kind == "PersistentVolume" || m.Head.Kind == "ConfigMap" ||
			m.Head.Kind == "Secret" || m.Head.Kind == "ServiceAccount")) ||
			(m.Head.Version == "batch/v1" && (m.Head.Kind == "Job" || m.Head.Kind == "CronJob")) ||
			(m.Head.Version == "networking.k8s.io/v1" && (m.Head.Kind == "Ingress" || m.Head.Kind == "NetworkPolicy")) {
			apiObj := new(K8sYamlStruct)
			err := yamlv2.Unmarshal([]byte(m.Content), apiObj)
			if err != nil {
				log.Println(err)
			}
			if apiObj.Metadata.Labels == nil {
				apiObj.Metadata.Labels = map[string]string{KEY: v}
			} else {
				apiObj.Metadata.Labels[KEY] = v
			}
			var obj map[interface{}]interface{}
			err = yamlv2.Unmarshal([]byte(m.Content), &obj)
			if err != nil {
				log.Println(err)
			}
			for k, v := range obj {
				switch k.(type) {
				case string:
					if k == "metadata" {
						if v == nil {
							obj[k] = apiObj.Metadata
						} else {
							metadata := v.(map[interface{}]interface{})
							metadata["labels"] = apiObj.Metadata.Labels
							obj[k] = metadata
						}
					}
				}
			}
			content, err := yamlv2.Marshal(obj)
			if err != nil {
				log.Println(err)
			}
			manifests[index].Content = string(content)
		}
	}

	// Aggregate all valid manifests into one big doc.
	fileWritten := make(map[string]bool)

	if includeCrds {
		for _, crd := range ch.CRDObjects() {
			if outputDir == "" {
				fmt.Fprintf(b, "---\n# Source: %s\n%s\n", crd.Name, string(crd.File.Data[:]))
			} else {
				err = writeToFile(outputDir, crd.Filename, string(crd.File.Data[:]), fileWritten[crd.Name])
				if err != nil {
					return hs, b, "", err
				}
				fileWritten[crd.Name] = true
			}
		}
	}

	for _, m := range manifests {
		if outputDir == "" {
			fmt.Fprintf(b, "---\n# Source: %s\n%s\n", m.Name, m.Content)
		} else {
			newDir := outputDir
			if useReleaseName {
				newDir = filepath.Join(outputDir, releaseName)
			}
			// NOTE: We do not have to worry about the post-renderer because
			// output dir is only used by `helm template`. In the next major
			// release, we should move this logic to template only as it is not
			// used by install or upgrade
			err = writeToFile(newDir, m.Name, m.Content, fileWritten[m.Name])
			if err != nil {
				return hs, b, "", err
			}
			fileWritten[m.Name] = true
		}
	}

	if pr != nil {
		b, err = pr.Run(b)
		if err != nil {
			return hs, b, notes, errors.Wrap(err, "error while running post render on files")
		}
	}

	return hs, b, notes, nil
}

// RESTClientGetter gets the rest client
type RESTClientGetter interface {
	ToRESTConfig() (*rest.Config, error)
	ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error)
	ToRESTMapper() (meta.RESTMapper, error)
}

// DebugLog sets the logger that writes debug strings
type DebugLog func(format string, v ...interface{})

// capabilities builds a Capabilities from discovery information.
func (c *Configuration) getCapabilities() (*chartutil.Capabilities, error) {
	if c.Capabilities != nil {
		return c.Capabilities, nil
	}
	dc, err := c.RESTClientGetter.ToDiscoveryClient()
	if err != nil {
		return nil, errors.Wrap(err, "could not get Kubernetes discovery client")
	}
	// force a discovery cache invalidation to always fetch the latest server version/capabilities.
	dc.Invalidate()
	kubeVersion, err := dc.ServerVersion()
	if err != nil {
		return nil, errors.Wrap(err, "could not get server version from Kubernetes")
	}
	// Issue #6361:
	// Client-Go emits an error when an API service is registered but unimplemented.
	// We trap that error here and print a warning. But since the discovery client continues
	// building the API object, it is correctly populated with all valid APIs.
	// See https://github.com/kubernetes/kubernetes/issues/72051#issuecomment-521157642
	apiVersions, err := GetVersionSet(dc)
	if err != nil {
		if discovery.IsGroupDiscoveryFailedError(err) {
			c.Log("WARNING: The Kubernetes server has an orphaned API service. Server reports: %s", err)
			c.Log("WARNING: To fix this, kubectl delete apiservice <service-name>")
		} else {
			return nil, errors.Wrap(err, "could not get apiVersions from Kubernetes")
		}
	}

	c.Capabilities = &chartutil.Capabilities{
		APIVersions: apiVersions,
		KubeVersion: chartutil.KubeVersion{
			Version: kubeVersion.GitVersion,
			Major:   kubeVersion.Major,
			Minor:   kubeVersion.Minor,
		},
	}
	return c.Capabilities, nil
}

// KubernetesClientSet creates a new kubernetes ClientSet based on the configuration
func (c *Configuration) KubernetesClientSet() (kubernetes.Interface, error) {
	conf, err := c.RESTClientGetter.ToRESTConfig()
	if err != nil {
		return nil, errors.Wrap(err, "unable to generate config for kubernetes client")
	}

	return kubernetes.NewForConfig(conf)
}

// Now generates a timestamp
//
// If the configuration has a Timestamper on it, that will be used.
// Otherwise, this will use time.Now().
func (c *Configuration) Now() time.Time {
	return Timestamper()
}

func (c *Configuration) releaseContent(name string, version int) (*release.Release, error) {
	if err := chartutil.ValidateReleaseName(name); err != nil {
		return nil, errors.Errorf("releaseContent: Release name is invalid: %s", name)
	}

	if version <= 0 {
		return c.Releases.Last(name)
	}

	return c.Releases.Get(name, version)
}

// GetVersionSet retrieves a set of available k8s API versions
func GetVersionSet(client discovery.ServerResourcesInterface) (chartutil.VersionSet, error) {
	groups, resources, err := client.ServerGroupsAndResources()
	if err != nil && !discovery.IsGroupDiscoveryFailedError(err) {
		return chartutil.DefaultVersionSet, errors.Wrap(err, "could not get apiVersions from Kubernetes")
	}

	// FIXME: The Kubernetes test fixture for cli appears to always return nil
	// for calls to Discovery().ServerGroupsAndResources(). So in this case, we
	// return the default API list. This is also a safe value to return in any
	// other odd-ball case.
	if len(groups) == 0 && len(resources) == 0 {
		return chartutil.DefaultVersionSet, nil
	}

	versionMap := make(map[string]interface{})
	versions := []string{}

	// Extract the groups
	for _, g := range groups {
		for _, gv := range g.Versions {
			versionMap[gv.GroupVersion] = struct{}{}
		}
	}

	// Extract the resources
	var id string
	var ok bool
	for _, r := range resources {
		for _, rl := range r.APIResources {

			// A Kind at a GroupVersion can show up more than once. We only want
			// it displayed once in the final output.
			id = path.Join(r.GroupVersion, rl.Kind)
			if _, ok = versionMap[id]; !ok {
				versionMap[id] = struct{}{}
			}
		}
	}

	// Convert to a form that NewVersionSet can use
	for k := range versionMap {
		versions = append(versions, k)
	}

	return chartutil.VersionSet(versions), nil
}

// recordRelease with an update operation in case reuse has been set.
func (c *Configuration) recordRelease(r *release.Release) {
	if err := c.Releases.Update(r); err != nil {
		c.Log("warning: Failed to update release %s: %s", r.Name, err)
	}
}

// Init initializes the action configuration
func (c *Configuration) Init(getter genericclioptions.RESTClientGetter, namespace, helmDriver string, log DebugLog) error {
	kc := kube.New(getter)
	kc.Log = log

	lazyClient := &lazyClient{
		namespace: namespace,
		clientFn:  kc.Factory.KubernetesClientSet,
	}

	var store *storage.Storage
	switch helmDriver {
	case "secret", "secrets", "":
		d := driver.NewSecrets(newSecretClient(lazyClient))
		d.Log = log
		store = storage.Init(d)
	case "configmap", "configmaps":
		d := driver.NewConfigMaps(newConfigMapClient(lazyClient))
		d.Log = log
		store = storage.Init(d)
	case "memory":
		var d *driver.Memory
		if c.Releases != nil {
			if mem, ok := c.Releases.Driver.(*driver.Memory); ok {
				// This function can be called more than once (e.g., helm list --all-namespaces).
				// If a memory driver was already initialized, re-use it but set the possibly new namespace.
				// We re-use it in case some releases where already created in the existing memory driver.
				d = mem
			}
		}
		if d == nil {
			d = driver.NewMemory()
		}
		d.SetNamespace(namespace)
		store = storage.Init(d)
	case "sql":
		d, err := driver.NewSQL(
			os.Getenv("HELM_DRIVER_SQL_CONNECTION_STRING"),
			log,
			namespace,
		)
		if err != nil {
			panic(fmt.Sprintf("Unable to instantiate SQL driver: %v", err))
		}
		store = storage.Init(d)
	default:
		// Not sure what to do here.
		panic("Unknown driver in HELM_DRIVER: " + helmDriver)
	}

	c.RESTClientGetter = getter
	c.KubeClient = kc
	c.Releases = store
	c.Log = log

	return nil
}

type K8sYamlStruct struct {
	Metadata k8sYamlMetadata `yaml:"metadata"`
	Spec     Spec            `yaml:"spec"`
}

type k8sYamlMetadata struct {
	Labels map[string]string `yaml:"labels"`
}
type Spec struct {
	Selector Selector `yaml:"selector"`
	Template Template `yaml:"template"`
}
type Selector struct {
	MatchLabels map[string]string `yaml:"matchLabels"`
}
type Template struct {
	Metadata k8sYamlMetadata `yaml:"metadata"`
}
