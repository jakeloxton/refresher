package main

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"github.com/jpillora/opts"
	log "github.com/sirupsen/logrus"
	"io"
	"io/ioutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	CheckTime         time.Duration `opts:"help=how often to run the check in seconds"`
	ProductionLogging bool          `opts:"help=sets whether production logging is on (json)"`
	SourcesFile       string        `opts:"help=sets the filepath of the sources key=value file"`
	CheckThreshold    int           `opts:"help=how many times changes should be verified before a reload is triggered"`
	AnnotationKey     string        `opts:"help=the annotation key to be checked when a reload is triggered"`
	ReloadKey         string        `opts:"help=the annotation key to trigger a reload"`
}

type Source struct {
	id          string
	url         *url.URL
	hash        string
	changeCount int
}

var (
	sources          []Source
	kubernetesClient *kubernetes.Clientset
	config           Config
)

func restartDeployment(id string) error {
	deploymentList, err := kubernetesClient.AppsV1().Deployments("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return err
	}

	for _, deployment := range deploymentList.Items {
		if annotation, ok := deployment.Annotations[config.AnnotationKey]; ok {
			if annotation == id {
				patchData := fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"%s":"%d"}}}}}`,
					config.ReloadKey, time.Now().Unix())
				_, err := kubernetesClient.AppsV1().Deployments(deployment.Namespace).
					Patch(context.TODO(), deployment.Name, types.StrategicMergePatchType,
						[]byte(patchData), metav1.PatchOptions{FieldManager: "refresher.mrl"})
				if err != nil {
					return err
				} else {
					log.WithField("deploy", deployment.Name).Info("reloaded")
				}
			} else {
				log.WithField("deployment", deployment.Name).
					WithField("id", id).
					Info("found annotation but did not match")
			}
		}
	}

	return nil
}

// TODO: Replace with a proper config parser
func parseSourcesFile(configPath string) {
	log.Info("parsing sources list")

	file, err := os.Open(configPath)
	if err != nil {
		log.WithError(err).Fatal("could not open file")
	}
	defer file.Close()

	reader := bufio.NewReader(file)

	sources = make([]Source, 0)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			log.WithError(err).Fatal("could not parse line")
		}

		splitStrings := strings.Split(line, "=")
		if len(splitStrings) < 2 {
			log.WithError(err).Fatal("line has incorrect format")
		}

		tmpUrl, err := url.Parse(strings.TrimSpace(splitStrings[1]))
		if err != nil {
			log.WithError(err).Fatal("could not parse url")
		}

		tmpId := strings.TrimSpace(splitStrings[0])

		log.WithField("id", tmpId).Debug("adding source")
		sources = append(sources, Source{
			id:  tmpId,
			url: tmpUrl,
		})
	}

	log.Infof("parsed %d sources", len(sources))
}

func run() {
	for {
		log.Info("running check")

		for idx, val := range sources {
			hash := sha1.New()

			rsp, err := http.Get(val.url.String())
			if err != nil {
				log.WithError(err).
					WithField("id", val.id).
					Warning("could not reach url")
				continue
			}

			if rsp.StatusCode != http.StatusOK {
				log.WithError(err).
					WithField("id", val.id).
					WithField("status_code", rsp.StatusCode).
					Warning("could not reach url")
				continue
			}

			body, err := ioutil.ReadAll(rsp.Body)
			if err != nil {
				log.WithError(err).WithField("id", val.id).Warning("could not parse body")
				continue
			}

			hash.Write(body)
			tmpHash := hex.EncodeToString(hash.Sum(nil))

			if val.hash == "" {
				log.WithField("id", val.id).Info("no hash found, setting to new value")
				sources[idx].hash = tmpHash
			} else {
				if val.hash != tmpHash {
					log.WithField("id", val.id).
						WithField("check_count", val.changeCount).
						WithField("old_hash", val.hash).
						WithField("new_nash", tmpHash).
						Debug("different hash found")

					// confirm the change
					if val.changeCount < config.CheckThreshold {
						log.WithField("id", val.id).
							Info("incrementing count due to different hash")
						sources[idx].changeCount++
					} else {
						log.WithField("id", val.id).
							Info("change found, commencing reload")
						err := restartDeployment(val.id)
						if err != nil {
							log.WithField("id", val.id).WithError(err).Error("reload failed")
						} else {
							sources[idx].hash = tmpHash
							sources[idx].changeCount = 0
						}
					}
				} else {
					log.WithField("id", val.id).Debug("matched hash, skipping")
				}
			}
		}

		time.Sleep(config.CheckTime * time.Second)
	}
}

func main() {
	config = Config{
		CheckTime:         10,
		ProductionLogging: false,
		SourcesFile:       "/etc/refresher/refresher.conf",
		CheckThreshold:    3,
		AnnotationKey:     "refresher.mrl/source",
		ReloadKey:         "refresher.mrl/reloaded-at",
	}

	opts.New(&config).UseEnv().Parse()

	if config.ProductionLogging {
		log.SetFormatter(&log.JSONFormatter{})
		log.SetLevel(log.InfoLevel)
	} else {
		log.SetLevel(log.DebugLevel)
	}

	log.Info("starting up")

	kubernetesConfig, err := rest.InClusterConfig()
	if err != nil {
		log.WithError(err).Fatal("failed to create kubernetes config")
	}

	kubernetesClient, err = kubernetes.NewForConfig(kubernetesConfig)
	if err != nil {
		log.WithError(err).Fatal("failed to create kubernetes client")
	}

	parseSourcesFile(config.SourcesFile)
	run()
}
