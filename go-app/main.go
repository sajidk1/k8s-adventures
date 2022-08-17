package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"strings"
	"syscall"

	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	docs "github.com/sajid-khan-js/k8s-adventures/go-app/docs"
	"github.com/sajid-khan-js/k8s-adventures/go-app/modules/k8sclient"
	swaggerFiles "github.com/swaggo/files"     // swagger embed files
	ginSwagger "github.com/swaggo/gin-swagger" // gin-swagger middleware
)

// Pod model info
// @Description Pod information
type Pod struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// Namespace model info
// @Description Namespace information
type Namespace struct {
	// https://github.com/go-playground/validator and https://kubernetes.io/docs/concepts/overview/working-with-objects/names/#dns-subdomain-names
	Name string `json:"name" binding:"required,hostname_rfc1123"`
	Pods []Pod  `json:"pods"`
}

/*

Test data:

var namespaces = []Namespace{
	{Name: "default", Pods: []Pod{
		Pod{Name: "nginx", Status: "Running"},
		Pod{Name: "httpbin", Status: "Pending"}}},
	{Name: "kube-system", Pods: []Pod{
		Pod{Name: "coredns-558bd4d5db-gmbdd", Status: "Running"},
		Pod{Name: "etcd-docker-desktop", Status: "Running"},
		Pod{Name: "kube-scheduler-docker-desktop", Status: "Running"}}},
	{Name: "app", Pods: []Pod{
		Pod{Name: "my-app", Status: "CrashLoopBackOff"}}},
}

*/

// @title Gin Swagger Example API
// @version 2.0
// @description This is a sample server server.
// @termsOfService http://swagger.io/terms/

// @contact.name API Support
// @contact.url http://www.swagger.io/support
// @contact.email support@swagger.io

// @license.name Apache 2.0
// @license.url http://www.apache.org/licenses/LICENSE-2.0.html

// @host localhost:8080
// @BasePath /namespaces
// @schemes http
func setupRouter() *gin.Engine {

	router := gin.Default()

	router.GET("/namespaces", getNamespaces)
	router.GET("/namespaces/:name", getNamespaceByName)
	router.POST("/namespaces", postNamespace)

	router.GET("/healthz", func(c *gin.Context) { c.Status(http.StatusOK) })

	// Mock readiness e.g. app might need to connect to db, load data, cache warm etc.
	// https://blog.gopheracademy.com/advent-2017/kubernetes-ready-service/
	isReady := &atomic.Value{}
	isReady.Store(false)
	// go routine, not blocking
	go func() {
		log.Printf("Readyz probe is negative by default...")
		time.Sleep(15 * time.Second)
		isReady.Store(true)
		log.Printf("Readyz probe is positive.")
	}()
	router.GET("/readyz", func(c *gin.Context) {
		if isReady == nil || !isReady.Load().(bool) {
			c.Status(http.StatusServiceUnavailable)
		} else {
			c.Status(http.StatusOK)
		}

	})

	// localhost:8080/swagger/index.html
	docs.SwaggerInfo.BasePath = "/"
	router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	return router
}
func main() {
	/*
		Graceful shutdown:
		https://cloud.google.com/blog/products/containers-kubernetes/kubernetes-best-practices-terminating-with-grace
		https://github.com/gin-gonic/examples/blob/master/graceful-shutdown/graceful-shutdown/notify-with-context/server.go
		https://gin-gonic.com/docs/examples/graceful-restart-or-stop/
	*/
	// Create context that listens for the interrupt signal from the OS.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	router := setupRouter()

	srv := &http.Server{
		Addr:    ":8080",
		Handler: router,
	}

	// Initializing the server in a goroutine so that
	// it won't block the graceful shutdown handling below
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %s\n", err)
		}
	}()

	// Listen for the interrupt signal.
	<-ctx.Done()

	// Restore default behaviour on the interrupt signal and notify user of shutdown.
	stop()
	log.Printf("Shutting down gracefully, will give 10 seconds for existing requests")

	// The context is used to inform the server it has 10 seconds to finish
	// the request it is currently handling
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatal("Server forced to shutdown: ", err)
	}

	log.Printf("Server exiting")
}

// getNamespaces godoc
// @Summary List namespaces in a K8s cluster.
// @Description Get all namespaces and all Pods in a cluster.
// @Produce json
// @Success 200 {object} []Namespace
// @Router /namespaces/ [get]
// curl -v -L localhost:8080/namespaces
func getNamespaces(c *gin.Context) {

	clientSet, err := k8sclient.InitClient()
	if err != nil {
		log.Print(err)
		c.IndentedJSON(http.StatusInternalServerError, gin.H{"message": "Something went wrong on our side"})
		return
	}

	rawNamespaces, err := k8sclient.GetNamespaces(*clientSet)
	if err != nil {
		log.Print(err)
		c.IndentedJSON(http.StatusInternalServerError, gin.H{"message": "Something went wrong on our side"})
		return
	}

	var namespaces []Namespace

	for _, ns := range rawNamespaces {
		var namespace Namespace
		namespace.Name = ns

		// TODO share this code with getNamespaceByName
		podsInNamespace, err := k8sclient.GetPods(*clientSet, ns)
		if err != nil {
			log.Print(err)
			c.IndentedJSON(http.StatusInternalServerError, gin.H{"message": "Something went wrong on our side"})
			return
		}

		for i, n := range podsInNamespace {
			var p Pod
			p.Name = i
			p.Status = n
			namespace.Pods = append(namespace.Pods, p)
		}

		namespaces = append(namespaces, namespace)
	}

	// Return serialized namespaces
	c.IndentedJSON(http.StatusOK, namespaces)
}

// getNamespaceByName godoc
// @Summary Get a K8s namespace and it's Pods.
// @Description Get Pods in a namespace.
// @Param        name   path      string  true  "Namespace name"
// @Produce json
// @Success 200 {object} Namespace
// @Router /namespaces/{name} [get]
// curl -v -L localhost:8080/namespaces/default
func getNamespaceByName(c *gin.Context) {

	name := c.Param("name")

	clientSet, err := k8sclient.InitClient()
	if err != nil {
		log.Print(err)
		c.IndentedJSON(http.StatusInternalServerError, gin.H{"message": "Something went wrong on our side"})
		return
	}

	podsInNamespace, err := k8sclient.GetPods(*clientSet, name)
	if err != nil {
		switch {
		case strings.Contains(err.Error(), "not found"):
			log.Print(err)
			c.IndentedJSON(http.StatusNotFound, gin.H{"message": "Namespace '" + name + "' not found"})
			return
		default:
			log.Print(err)
			c.IndentedJSON(http.StatusInternalServerError, gin.H{"message": "Something went wrong on our side"})
			return
		}
	}

	if len(podsInNamespace) > 0 {
		var ns Namespace
		ns.Name = name
		for i, n := range podsInNamespace {
			var p Pod
			p.Name = i
			p.Status = n
			ns.Pods = append(ns.Pods, p)
		}

		c.IndentedJSON(http.StatusOK, ns)

	} else {
		// Probably should just return the empty slice of Pods to be consistent, but leaving this here for demo purposes
		c.IndentedJSON(http.StatusOK, gin.H{"message": "Namespace '" + name + "' has no Pods"})
	}

}

// postNamespace godoc
// @Summary Create a new namespace.
// @Description Create a new namespace in the cluster.
// @Param        name   body      Namespace  true  "Namespace name"
// @Accept  json
// @Produce json
// @Success 200 {object} Namespace
// @Router /namespaces [post]
/*
curl -v -L 'localhost:8080/namespaces/' \
--header 'Content-Type: application/json' \
--data-raw '{
    "name": "mynamespace"
}'
*/
func postNamespace(c *gin.Context) {

	var newNamespace Namespace

	// Validate based on JSON struct tags in Namespace struct
	if err := c.ShouldBindJSON(&newNamespace); err != nil {
		// TODO differentiate between validation errors e.g. missing required key (name) and incorrect value (namespace name not RFC 1123)
		c.JSON(http.StatusBadRequest, gin.H{"error": "Namespace name must comply with RFC 1123"})
		return
	}

	name := newNamespace.Name

	clientSet, err := k8sclient.InitClient()
	if err != nil {
		log.Print(err)
		c.IndentedJSON(http.StatusInternalServerError, gin.H{"message": "Something went wrong on our side"})
		return
	}

	err = k8sclient.CreateNamespace(*clientSet, name)

	if err != nil {
		switch {
		case strings.Contains(err.Error(), "already exists"):
			log.Print(err)
			c.IndentedJSON(http.StatusBadRequest, gin.H{"message": "Namespace '" + name + "' already exists"})
			return
		default:
			log.Print(err)
			c.IndentedJSON(http.StatusInternalServerError, gin.H{"message": "Something went wrong on our side"})
			return
		}
	}

	c.IndentedJSON(http.StatusCreated, newNamespace)
}
