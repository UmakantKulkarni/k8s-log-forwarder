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

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// NewHTTPSender initializes an HTTPSender with a timeout.
func NewHTTPSender(url string) *HTTPSender {
	return &HTTPSender{url: url, client: &http.Client{Timeout: 5 * time.Second}}
}

// Write sends a log line to the remote endpoint.
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

// LogStreamer streams logs for one pod/container based on mode.
type LogStreamer struct {
	clientset *kubernetes.Clientset
	namespace string
	pod       string
	container string
	sender    *HTTPSender
	stopCh    chan struct{}
	lastErr   time.Time
	mode      string
	sinceSec  int64
}

// Start begins the log streaming in a goroutine.
func (ls *LogStreamer) Start() {
	go ls.run()
}

// run continuously fetches logs per the configured mode.
func (ls *LogStreamer) run() {
	for {
		select {
		case <-ls.stopCh:
			return
		default:
		}

		// Build PodLogOptions based on mode
		opts := &v1.PodLogOptions{Container: ls.container, Follow: true}
		switch ls.mode {
		case "real":
			// Only new logs from now onward
			now := metav1.NewTime(time.Now())
			opts.SinceTime = &now
		case "since":
			opts.SinceSeconds = &ls.sinceSec
		case "all":
			// No filters: full history + follow
		default:
			// Unrecognized mode: default to 'all'
		}

		req := ls.clientset.CoreV1().Pods(ls.namespace).GetLogs(ls.pod, opts)
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

// Stop terminates the log streaming.
func (ls *LogStreamer) Stop() {
	close(ls.stopCh)
}

func main() {
	// Flags and environment defaults
	ns := flag.String("namespace", os.Getenv("TARGET_NAMESPACE"), "Kubernetes namespace to watch")
	rm := flag.String("remote-url", os.Getenv("REMOTE_URL"), "HTTP endpoint to send logs")
	selector := flag.String("selector", os.Getenv("LABEL_SELECTOR"), "Label selector for pods")
	containerRegexStr := flag.String("container-regex", os.Getenv("CONTAINER_REGEX"), "Regex to match container names")
	mode := flag.String("mode", "all", "Log mode: 'all', 'real', or 'since'")
	sinceSec := flag.Int64("since", 0, "When mode='since', stream logs newer than this many seconds before now")
	flag.Parse()

	// Validate flags
	if *mode != "all" && *mode != "real" && *mode != "since" {
		log.Fatalf("invalid mode: %s", *mode)
	}
	if *mode == "since" && *sinceSec <= 0 {
		log.Fatalf("--since must be > 0 when mode='since'")
	}
	if *ns == "" || *rm == "" {
		log.Fatalf("namespace and remote-url must be specified")
	}

	// Kubernetes client
	cfg, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("cannot get in-cluster config: %v", err)
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Fatalf("cannot create clientset: %v", err)
	}

	// Container name regex
	var regex *regexp.Regexp
	if *containerRegexStr != "" {
		regex, err = regexp.Compile(*containerRegexStr)
		if err != nil {
			log.Fatalf("invalid container regex: %v", err)
		}
	}

	// HTTP sender
	sender := NewHTTPSender(*rm)

	// Map to track active streams
	lsMap := make(map[string]*LogStreamer)
	var mu sync.Mutex

	// Pod informer factory
	factory := informers.NewSharedInformerFactoryWithOptions(clientset, 0,
		informers.WithNamespace(*ns),
		informers.WithTweakListOptions(func(o *metav1.ListOptions) {
			if *selector != "" {
				o.LabelSelector = *selector
			}
		}),
	)
	podInformer := factory.Core().V1().Pods().Informer()

	// Event handlers for pod add/delete
	handler := cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			pod := obj.(*v1.Pod)
			mu.Lock()
			defer mu.Unlock()
			for _, c := range append(pod.Spec.InitContainers, pod.Spec.Containers...) {
				if regex != nil && !regex.MatchString(c.Name) {
					continue
				}
				key := fmt.Sprintf("%s/%s", pod.Name, c.Name)
				if _, exists := lsMap[key]; !exists {
					ls := &LogStreamer{
						clientset: clientset,
						namespace: *ns,
						pod:       pod.Name,
						container: c.Name,
						sender:    sender,
						stopCh:    make(chan struct{}),
						mode:      *mode,
						sinceSec:  *sinceSec,
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
				key := fmt.Sprintf("%s/%s", pod.Name, c.Name)
				if ls, exists := lsMap[key]; exists {
					ls.Stop()
					delete(lsMap, key)
				}
			}
		},
	}

	podInformer.AddEventHandler(handler)

	// Start informer
	stopCh := make(chan struct{})
	factory.Start(stopCh)
	factory.WaitForCacheSync(stopCh)

	// Block forever
	<-stopCh
}
