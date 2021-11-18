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
	DryRun            bool          `opts:"help=flag stating whether or not we should skip the kubernetes api calls"`
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

	if len(sources) < 1 {
		log.Fatal("config file contained no sources")
	}

	log.Infof("parsed %d sources", len(sources))
}

func watchSource(notify chan string, source Source) {
	for {
		time.Sleep(config.CheckTime * time.Second)

		hash := sha1.New()

		rsp, err := http.Get(source.url.String())
		if err != nil {
			log.WithError(err).
				WithField("id", source.id).
				Warning("could not reach url")
			continue
		}

		if rsp.StatusCode != http.StatusOK {
			log.WithError(err).
				WithField("id", source.id).
				WithField("status_code", rsp.StatusCode).
				Warning("could not reach url")
			continue
		}

		body, err := ioutil.ReadAll(rsp.Body)
		if err != nil {
			log.WithError(err).WithField("id", source.id).Warning("could not parse body")
			continue
		}

		hash.Write(body)
		tmpHash := hex.EncodeToString(hash.Sum(nil))

		if source.hash == "" {
			log.WithField("id", source.id).Info("no hash found, setting to new value")
			source.hash = tmpHash
		} else {
			if source.hash != tmpHash {
				log.WithField("id", source.id).
					WithField("check_count", source.changeCount).
					WithField("old_hash", source.hash).
					WithField("new_nash", tmpHash).
					Debug("different hash found")

				// confirm the change
				if source.changeCount < config.CheckThreshold {
					log.WithField("id", source.id).Info("incrementing count due to different hash")
					source.changeCount++
				} else {
					log.WithField("id", source.id).Info("change found, commencing reload")

					notify <- source.id
					source.hash = tmpHash
					source.changeCount = 0
				}
			} else {
				log.WithField("id", source.id).Debug("matched hash, skipping")
			}
		}
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
		DryRun:            true,
	}

	opts.New(&config).UseEnv().Parse()

	if config.ProductionLogging {
		log.SetFormatter(&log.JSONFormatter{})
		log.SetLevel(log.InfoLevel)
	} else {
		log.SetLevel(log.DebugLevel)
	}

	log.Info("starting up")

	if !config.DryRun {
		kubernetesConfig, err := rest.InClusterConfig()
		if err != nil {
			log.WithError(err).Fatal("failed to create kubernetes config")
		}

		kubernetesClient, err = kubernetes.NewForConfig(kubernetesConfig)
		if err != nil {
			log.WithError(err).Fatal("failed to create kubernetes client")
		}
	}

	parseSourcesFile(config.SourcesFile)

	notify := make(chan string)

	for _, source := range sources {
		go watchSource(notify, source)
	}

	for {
		annotationID := <- notify

		if annotationID == "" {
			log.Warning("received empty data on notify channel")
		}

		if !config.DryRun {
			err := restartDeployment(annotationID)
			if err != nil {
				log.WithField("id", annotationID).WithError(err).Error("reload failed")
			}
		} else {
			log.WithField("id", annotationID).Info("reloading skipped as dry run flag is set")
		}
	}
}
