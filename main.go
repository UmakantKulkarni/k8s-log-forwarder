package main

import (
	"bufio"
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

// HTTPSender posts log lines to a remote HTTP endpoint.
type HTTPSender struct {
	url     string
	client  *http.Client
	lastErr time.Time
}

const errorLogInterval = 30 * time.Second

func NewHTTPSender(url string) *HTTPSender {
	return &HTTPSender{url: url, client: &http.Client{Timeout: 5 * time.Second}}
}

func (h *HTTPSender) Write(p []byte) {
	req, err := http.NewRequest("POST", h.url, bytes.NewReader(p))
	if err != nil {
		log.Printf("create request error: %v", err)
		return
	}
	resp, err := h.client.Do(req)
	if err != nil {
		if time.Since(h.lastErr) > errorLogInterval {
			log.Printf("send error: %v", err)
			h.lastErr = time.Now()
		}
		return
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

// LogStreamer streams logs of a specific pod/container.
type LogStreamer struct {
	clientset *kubernetes.Clientset
	namespace string
	pod       string
	container string
	sender    *HTTPSender
	stopCh    chan struct{}
	lastErr   time.Time
}

func (ls *LogStreamer) Start() {
	go ls.run()
}

func (ls *LogStreamer) run() {
	for {
		select {
		case <-ls.stopCh:
			return
		default:
		}

		req := ls.clientset.CoreV1().Pods(ls.namespace).GetLogs(ls.pod, &v1.PodLogOptions{Container: ls.container, Follow: true})
		stream, err := req.Stream(context.Background())
		if err != nil {
			if time.Since(ls.lastErr) > errorLogInterval {
				log.Printf("log stream error for %s/%s: %v", ls.pod, ls.container, err)
				ls.lastErr = time.Now()
			}
			time.Sleep(2 * time.Second)
			continue
		}
		scanner := bufio.NewScanner(stream)
		for scanner.Scan() {
			line := fmt.Sprintf("%s/%s %s\n", ls.pod, ls.container, scanner.Text())
			ls.sender.Write([]byte(line))
		}
		stream.Close()
		if err := scanner.Err(); err != nil {
			if time.Since(ls.lastErr) > errorLogInterval {
				log.Printf("scanner error for %s/%s: %v", ls.pod, ls.container, err)
				ls.lastErr = time.Now()
			}
		}
		time.Sleep(1 * time.Second)
	}
}

func (ls *LogStreamer) Stop() {
	close(ls.stopCh)
}

func main() {
	ns := flag.String("namespace", os.Getenv("TARGET_NAMESPACE"), "namespace to watch")
	remote := flag.String("remote-url", os.Getenv("REMOTE_URL"), "HTTP endpoint to send logs")
	selector := flag.String("selector", os.Getenv("LABEL_SELECTOR"), "label selector for pods")
	containerRegexStr := flag.String("container-regex", os.Getenv("CONTAINER_REGEX"), "regex to match container names")
	flag.Parse()

	if *ns == "" || *remote == "" {
		log.Fatalf("namespace and remote-url must be specified")
	}

	cfg, err := rest.InClusterConfig()
	if err != nil {
		cfg, err = rest.InClusterConfig()
		if err != nil {
			log.Fatalf("cannot get in cluster config: %v", err)
		}
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Fatalf("cannot create clientset: %v", err)
	}

	regex, err := regexp.Compile(*containerRegexStr)
	if *containerRegexStr != "" && err != nil {
		log.Fatalf("invalid container regex: %v", err)
	}

	sender := NewHTTPSender(*remote)

	lsMap := make(map[string]*LogStreamer)
	var mu sync.Mutex

	factory := informers.NewSharedInformerFactoryWithOptions(clientset, 0,
		informers.WithNamespace(*ns),
		informers.WithTweakListOptions(func(o *metav1.ListOptions) {
			if *selector != "" {
				o.LabelSelector = *selector
			}
		}))

	podInformer := factory.Core().V1().Pods().Informer()

	handler := cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			pod := obj.(*v1.Pod)
			mu.Lock()
			defer mu.Unlock()
			for _, c := range append(pod.Spec.InitContainers, pod.Spec.Containers...) {
				if regex != nil && !regex.MatchString(c.Name) {
					continue
				}
				key := pod.Name + "/" + c.Name
				if _, exists := lsMap[key]; !exists {
					ls := &LogStreamer{
						clientset: clientset,
						namespace: *ns,
						pod:       pod.Name,
						container: c.Name,
						sender:    sender,
						stopCh:    make(chan struct{}),
					}
					ls.Start()
					lsMap[key] = ls
				}
			}
		},
		DeleteFunc: func(obj interface{}) {
			pod := obj.(*v1.Pod)
			mu.Lock()
			defer mu.Unlock()
			for _, c := range append(pod.Spec.InitContainers, pod.Spec.Containers...) {
				key := pod.Name + "/" + c.Name
				if ls, exists := lsMap[key]; exists {
					ls.Stop()
					delete(lsMap, key)
				}
			}
		},
	}

	podInformer.AddEventHandler(handler)

	stopCh := make(chan struct{})
	defer close(stopCh)
	factory.Start(stopCh)
	factory.WaitForCacheSync(stopCh)
	<-stopCh
}
