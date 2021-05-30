package main

import (
	"fmt"
	"log"
	"math/rand"
	"time"

	"context"
	"errors"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	listersv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	metrics "k8s.io/metrics/pkg/client/clientset/versioned"
)

const schedulerName = "label-affinity-scheduler"

//POJO de apoio para realizar a gestão de PODS e o processo de Bind
type Scheduler struct {
	clientset  *kubernetes.Clientset
	context    context.Context
	podQueue   chan *v1.Pod
	nodeLister listersv1.NodeLister
}

//inicio da execução, disponibilizando o Custom Scheduler como um Deployment
func main() {
	log.Println("[" + schedulerName + "] Custom Scheduler Ready!")

	rand.Seed(time.Now().Unix())

	podQueue := make(chan *v1.Pod, 300)
	defer close(podQueue)

	quit := make(chan struct{})
	defer close(quit)

	//construção do scheduler
	scheduler := buildScheduler(podQueue, quit)
	scheduler.run(quit)
}

//construção do objeto Scheduler, que será utilizado durante disparo dos eventos de schedule do Kubernetes
func buildScheduler(podQueue chan *v1.Pod, quit chan struct{}) Scheduler {
	config, err := rest.InClusterConfig()

	if err != nil {
		log.Fatal(err)
	}

	clientset, err := kubernetes.NewForConfig(config)

	if err != nil {
		log.Fatal(err)
	}

	context := context.TODO()

	//construção dos Listeners, para monitoramento dos novos NODES/PODS adicionados ao Cluster
	nodeListener := buildNodeAndPodListeners(clientset, podQueue, quit)

	//retornando nova instância da classe Scheduler
	return Scheduler{
		clientset:  clientset,
		context:    context,
		podQueue:   podQueue,
		nodeLister: nodeListener,
	}
}

//Ao receber um evento de novo NODE/POD adicionado ao cluster, adiciona-los as variáveis de apoio
func buildNodeAndPodListeners(clientset *kubernetes.Clientset, podQueue chan *v1.Pod, quit chan struct{}) listersv1.NodeLister {
	factory := informers.NewSharedInformerFactory(clientset, 0)

	//adicionando evento p/ monitorar os NODES
	nodeInformer := factory.Core().V1().Nodes()
	nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			node, nodeOk := obj.(*v1.Node)

			if !nodeOk {
				log.Println("this is not a node")

				return
			}

			log.Printf("New Node Added to Store: %s", node.GetName())
		},
	})

	//adicionando evento p/ monitorar os PODS
	podInformer := factory.Core().V1().Pods()
	podInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			pod, podOk := obj.(*v1.Pod)

			if !podOk {
				log.Println("this is not a pod")
				return
			}

			if pod.Spec.NodeName == "" && pod.Spec.SchedulerName == schedulerName {
				podQueue <- pod
			}
		},
	})

	factory.Start(quit)

	//retornando a variável ref. ao Listener de NODES, necessária para realizar o calculo futuro de afinidade com os PODS
	return nodeInformer.Lister()
}

//método executado após construção do scheduler
func (scheduler *Scheduler) run(quit chan struct{}) {
	wait.Until(scheduler.schedulePodInQueue, 0, quit)
}

//Realizando o "agendamento" do POD a um nó computacional
func (scheduler *Scheduler) schedulePodInQueue() {
	podQueue := <-scheduler.podQueue
	fmt.Println("found a pod to schedule: ", podQueue.Namespace, "/", podQueue.Name)

	//encontrando o nó computacional com a maior afinidade baseado nas Labels definidas e priorizado pela maior capacidade computacional ociosa
	nodeForPodBind, err := scheduler.findNodeForPodBind(podQueue)

	if err != nil {
		log.Println("cannot find node that fits pod", err.Error())

		return
	}

	//realizando o "bind" do POD ao nó computacional
	err = scheduler.bindPodOnNode(podQueue, nodeForPodBind)

	if err != nil {
		log.Println("failed to bind pod", err.Error())

		return
	}

	//log no console ref. ao bind realizado
	message := fmt.Sprintf("Placed pod [%s/%s] on %s\n", podQueue.Namespace, podQueue.Name, nodeForPodBind)

	//gerando eventos no console do Kubernetes para acompanhar o custom scheduler
	err = scheduler.emitEvent(podQueue, message)

	if err != nil {
		log.Println("failed to emit scheduled event", err.Error())

		return
	}

	fmt.Println(message)
}

//Encontrando o nó computacional participante do cluster que possui a maior afinidade com o POD recebido
//obs.: Em caso de mais de um registro, será priorizado aquele com a maior capacidade computacional ociosa
func (scheduler *Scheduler) findNodeForPodBind(pod *v1.Pod) (string, error) {
	//obtendo a lista de nodes (a partir do listener) disponíveis
	listOfNodes, err := scheduler.nodeLister.List(labels.Everything())

	if err != nil {
		return "", err
	}

	//listando os nós computacionais a partir da afinidade de labels
	listOfFilteredNodes := scheduler.listNodesHighestLabelAffinity(listOfNodes, pod)

	//nenhum nó compativel :(
	if len(listOfFilteredNodes) == 0 {
		return "", errors.New("failed to find node that fits pod")
	}

	//aplicando estratégia de rankear os nodes de acordo com sua capacidade ociosa
	mapOfNodePriorities := scheduler.buildNodePriority(listOfFilteredNodes, pod)
	//percorrendo o mapa de prioridades para escolher o nó com maior capacidade ociosa, dentre aqueles que já passaram pelas regras de afinidade das labels
	nodeForPodBind := scheduler.findBestNode(mapOfNodePriorities)

	return nodeForPodBind, nil
}

//listando os nós computacionais de acordo com a afinidade de labels entre PODS e NODES
func (scheduler *Scheduler) listNodesHighestLabelAffinity(listOfNodes []*v1.Node, pod *v1.Pod) []*v1.Node {
	listOfFilteredNodes := make([]*v1.Node, 0)

	//percorrendo as labels vinculadas ao POD
	for podLabelKey, podLabelValue := range pod.ObjectMeta.Labels {
		log.Println("Pod Label -> " + podLabelKey + "/" + podLabelValue)
	}

	for _, node := range listOfNodes {
		//percorrendo as labels vinculadas ao NODE
		for nodeLabelKey, nodeLabelValue := range node.ObjectMeta.Labels {
			log.Println("Node Label -> " + nodeLabelKey + "/" + nodeLabelValue)
		}

		//TODO implementar neste ponto o check de labels
		//despresar labels que não sejam do mesmo DNS
		//verificar operador definido no valor do label dentro do POD
		//-- https://docs.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_comparison_operators?view=powershell-7.1
		//-- default será EQUAL

		listOfFilteredNodes = append(listOfFilteredNodes, node)
		// if s.predicatesApply(node, pod) {
		// 	filteredNodes = append(filteredNodes, node)
		// }
	}

	log.Println("nodes that fit:")

	for _, node := range listOfFilteredNodes {
		log.Println(node.Name)
	}

	return listOfFilteredNodes
}

// Deprecated: método não será utilizado, herança do "random-scheduler"
func (scheduler *Scheduler) predicatesApply(node *v1.Node, pod *v1.Pod) bool {
	// for _, predicate := range scheduler.predicates {
	// 	if !predicate(node, pod) {
	// 		return false
	// 	}
	// }

	return true
}

//gerando eventos no console do Kubernetes para acompanhamento
func (scheduler *Scheduler) emitEvent(p *v1.Pod, message string) error {
	timestamp := time.Now().UTC()

	_, err := scheduler.clientset.CoreV1().Events(p.Namespace).Create(context.TODO(), &v1.Event{
		Count:          1,
		Message:        message,
		Reason:         "Scheduled",
		LastTimestamp:  metav1.NewTime(timestamp),
		FirstTimestamp: metav1.NewTime(timestamp),
		Type:           "Normal",
		Source: v1.EventSource{
			Component: schedulerName,
		},
		InvolvedObject: v1.ObjectReference{
			Kind:      "Pod",
			Name:      p.Name,
			Namespace: p.Namespace,
			UID:       p.UID,
		},
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: p.Name + "-",
		},
	}, metav1.CreateOptions{})

	if err != nil {
		return err
	}

	return nil
}

//construindo a prioridade dos NODES a partir de sua capacidade computacional ociosa
func (scheduler *Scheduler) buildNodePriority(listOfNodes []*v1.Node, pod *v1.Pod) map[string]int {
	configMetrics, err := rest.InClusterConfig()

	if err != nil {
		log.Fatal(err)
	}

	metricsConfig, err := metrics.NewForConfig(configMetrics)

	mapOfNodePriorities := make(map[string]int)

	for _, node := range listOfNodes {
		//obtendo as métricas de uso ref. ao NODE atual
		nodeMetrics, err := metricsConfig.MetricsV1beta1().NodeMetricses().Get(context.TODO(), node.Name, metav1.GetOptions{})

		if err != nil {
			log.Fatal(err)
		}

		//declarando atributos para calculo de consumo dos recursos computacionais do NODE atual
		cpuCapacityQuantity, ok := node.Status.Capacity.Cpu().AsInt64()
		memCapacityQuantity, ok := node.Status.Capacity.Memory().AsInt64()
		cpuUsageQuantity, ok := nodeMetrics.Usage.Cpu().AsInt64()
		memUsageQuantity, ok := nodeMetrics.Usage.Memory().AsInt64()

		if !ok {
			continue
		}

		fmt.Printf("Node [%s] usage -> CPU [%d/%d] Memory [%d/%d]\n", node.Name, cpuCapacityQuantity, cpuUsageQuantity, memCapacityQuantity, memUsageQuantity)
		mapOfNodePriorities[node.Name] += int(cpuUsageQuantity)

		//codigo obsoleto
		// for _, priority := range scheduler.priorities {
		// 	mapOfNodePriorities[node.Name] += priority(node, pod)
		// }
	}

	log.Println("calculated priorities:")
	log.Println(mapOfNodePriorities)

	return mapOfNodePriorities
}

//percorre o mapa de prioridades dos nodes em busca daquele mais "indicado" a receber a instância do node
func (scheduler *Scheduler) findBestNode(mapOfNodePriorities map[string]int) string {
	var maxP int
	var bestNode string

	for node, p := range mapOfNodePriorities {
		if p > maxP {
			maxP = p
			bestNode = node
		}
	}

	return bestNode
}

//Deprecated: método herdado do "random-scheduler"
func randomPredicate(node *v1.Node, pod *v1.Pod) bool {
	r := rand.Intn(2)

	return r == 0
}

//Deprecated: método herdado do "random-scheduler"
func randomPriority(node *v1.Node, pod *v1.Pod) int {
	return rand.Intn(100)
}

//realizando o "bind" do POD ao nó computacional
//neste ponto todas as estratégias já foram aplicadas
//- lista de NODES com afinidade de labels
//- priorização de NODES com maior capacidade ociosa
func (scheduler *Scheduler) bindPodOnNode(pod *v1.Pod, nodeName string) error {
	return scheduler.clientset.CoreV1().Pods(pod.Namespace).Bind(context.TODO(), &v1.Binding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pod.Name,
			Namespace: pod.Namespace,
		},
		Target: v1.ObjectReference{
			APIVersion: "v1",
			Kind:       "Node",
			Name:       nodeName,
		},
	}, metav1.CreateOptions{})
}
