package main

import (
	"fmt"
	"log"
	"strings"
	"time"

	"context"
	"errors"

	appsV1 "k8s.io/api/apps/v1"
	coreV1 "k8s.io/api/core/v1"
	metaV1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	listersV1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	metrics "k8s.io/metrics/pkg/client/clientset/versioned"
)

const schedulerName = "label-affinity-scheduler"
const labelNameForDNSPrefix = "ppgcomp.unioeste.br/custom-scheduler-dns-prefix"

//POJO de apoio para realizar a gestão de PODS e o processo de Bind
type Scheduler struct {
	context                     context.Context
	clientset                   *kubernetes.Clientset
	podQueue                    chan *coreV1.Pod
	nodeLister                  listersV1.NodeLister
	deploymentOfCustomScheduler *appsV1.Deployment
}

//inicio da execução, disponibilizando o Custom Scheduler como um Deployment
func main() {
	log.Println("Custom Scheduler Ready! I'm " + schedulerName)

	//variavel Channel que armazenará os PODS criados pelo orquestrador e irá enfileirar os eventos de BIND
	podQueue := make(chan *coreV1.Pod, 300)
	defer close(podQueue)

	quit := make(chan struct{})
	defer close(quit)

	//construção do scheduler
	scheduler := buildScheduler(podQueue, quit)
	scheduler.run(quit)
}

//construção do objeto Scheduler, que será utilizado durante disparo dos eventos de schedule do Kubernetes
func buildScheduler(podQueue chan *coreV1.Pod, quit chan struct{}) Scheduler {
	config, err := rest.InClusterConfig()

	if err != nil {
		log.Fatal(err)
	}

	clientset, err := kubernetes.NewForConfig(config)

	if err != nil {
		log.Fatal(err)
	}

	context := context.TODO()

	//variável de apoio na obtenção de dados da API Go p/ Kubernetes
	factory := informers.NewSharedInformerFactory(clientset, 0)

	//encontrando o DEPLOYMENT que se refere ao Custom Scheduler atual
	deploymentOfCustomScheduler, err := clientset.AppsV1().Deployments(coreV1.NamespaceDefault).Get(context, schedulerName, metaV1.GetOptions{})

	if err != nil {
		log.Fatal(err)
	}

	//construção dos Listeners, para monitoramento dos novos NODES/PODS adicionados ao Cluster
	//adicionando evento p/ monitorar os NODES
	nodeInformer := factory.Core().V1().Nodes()
	nodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			node, nodeOk := obj.(*coreV1.Node)

			if !nodeOk {
				log.Println("this is not a node")

				return
			}

			log.Printf("New Node Added to Store: %s", node.GetName())
		},
	})

	nodeLister := nodeInformer.Lister()

	//adicionando evento p/ monitorar os PODS
	podInformer := factory.Core().V1().Pods()
	podInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			pod, podOk := obj.(*coreV1.Pod)

			if !podOk {
				log.Println("this is not a pod")

				return
			}

			//verificando se o POD esta sem nenhum NODE vinculado e se o mesmo esta atrelado ao Custom Scheduler atual
			if pod.Spec.NodeName == "" && pod.Spec.SchedulerName == deploymentOfCustomScheduler.Name {
				podQueue <- pod
			}
		},
	})

	factory.Start(quit)

	//criando nova instância do Custom Scheduler
	scheduler := Scheduler{
		context:                     context,
		clientset:                   clientset,
		podQueue:                    podQueue,
		nodeLister:                  nodeLister,
		deploymentOfCustomScheduler: deploymentOfCustomScheduler,
	}

	//retornando nova instância da classe Scheduler
	return scheduler
}

//método executado após interceptação de um novo POD
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
func (scheduler *Scheduler) findNodeForPodBind(pod *coreV1.Pod) (string, error) {
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
func (scheduler *Scheduler) listNodesHighestLabelAffinity(listOfNodes []*coreV1.Node, pod *coreV1.Pod) []*coreV1.Node {
	listOfFilteredNodes := make([]*coreV1.Node, 0)

	//definido qual o DNS definido p/ análise dos labels via prefixo
	dnsForLabelPrefix := scheduler.deploymentOfCustomScheduler.Labels[labelNameForDNSPrefix]

	//percorrendo as labels vinculadas ao POD
	log.Println("Pod labels for context " + scheduler.deploymentOfCustomScheduler.Name)

	for podLabelKey, podLabelValue := range pod.ObjectMeta.Labels {
		if !strings.HasPrefix(podLabelKey, dnsForLabelPrefix) {
			continue
		}

		log.Println(podLabelKey + "/" + podLabelValue)
	}

	for _, node := range listOfNodes {
		log.Println("Node: " + node.Namespace + "/" + node.Name + " labels for context " + scheduler.deploymentOfCustomScheduler.Name)

		//percorrendo as labels vinculadas ao NODE
		for nodeLabelKey, nodeLabelValue := range node.ObjectMeta.Labels {
			if !strings.HasPrefix(nodeLabelKey, dnsForLabelPrefix) {
				continue
			}

			log.Println(nodeLabelKey + "/" + nodeLabelValue)
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

	log.Println("Nodes that fit:")

	for _, node := range listOfFilteredNodes {
		fmt.Printf(node.Name + "\n")
	}

	return listOfFilteredNodes
}

// Deprecated: método não será utilizado, herança do "random-scheduler"
func (scheduler *Scheduler) predicatesApply(node *coreV1.Node, pod *coreV1.Pod) bool {
	// for _, predicate := range scheduler.predicates {
	// 	if !predicate(node, pod) {
	// 		return false
	// 	}
	// }

	return true
}

//gerando eventos no console do Kubernetes para acompanhamento
func (scheduler *Scheduler) emitEvent(p *coreV1.Pod, message string) error {
	timestamp := time.Now().UTC()

	_, err := scheduler.clientset.CoreV1().Events(p.Namespace).Create(context.TODO(), &coreV1.Event{
		Count:          1,
		Message:        message,
		Reason:         "Scheduled",
		LastTimestamp:  metaV1.NewTime(timestamp),
		FirstTimestamp: metaV1.NewTime(timestamp),
		Type:           "Normal",
		Source: coreV1.EventSource{
			Component: scheduler.deploymentOfCustomScheduler.Name,
		},
		InvolvedObject: coreV1.ObjectReference{
			Kind:      "Pod",
			Name:      p.Name,
			Namespace: p.Namespace,
			UID:       p.UID,
		},
		ObjectMeta: metaV1.ObjectMeta{
			GenerateName: p.Name + "-",
		},
	}, metaV1.CreateOptions{})

	if err != nil {
		return err
	}

	return nil
}

//construindo a prioridade dos NODES a partir de sua capacidade computacional ociosa
func (scheduler *Scheduler) buildNodePriority(listOfNodes []*coreV1.Node, pod *coreV1.Pod) map[string]int {
	configMetrics, err := rest.InClusterConfig()

	if err != nil {
		log.Fatal(err)
	}

	metricsConfig, err := metrics.NewForConfig(configMetrics)

	mapOfNodePriorities := make(map[string]int)

	for _, node := range listOfNodes {
		//obtendo as métricas de uso ref. ao NODE atual
		nodeMetrics, err := metricsConfig.MetricsV1beta1().NodeMetricses().Get(context.TODO(), node.Name, metaV1.GetOptions{})

		if err != nil {
			log.Fatal(err)
		}

		//declarando atributos para calculo de consumo dos recursos computacionais do NODE atual
		cpuCapacityQuantity, ok := node.Status.Capacity.Cpu().AsInt64()
		memCapacityQuantity, ok := node.Status.Capacity.Memory().AsInt64()
		diskCapacityQuantity, ok := node.Status.Capacity.Storage().AsInt64()
		diskEpheCapacityQuantity, ok := node.Status.Capacity.StorageEphemeral().AsInt64()
		cpuUsageQuantity, ok := nodeMetrics.Usage.Cpu().AsInt64()
		memUsageQuantity, ok := nodeMetrics.Usage.Memory().AsInt64()
		diskUsageQuantity, ok := nodeMetrics.Usage.Storage().AsInt64()
		diskEpheUsageQuantity, ok := nodeMetrics.Usage.StorageEphemeral().AsInt64()

		if !ok {
			continue
		}

		fmt.Printf("Node [%s] usage -> CPU [%d/%d] Memory [%d/%d] Disk [%d/%d] Disk Ephemeral [%d/%d]\n", node.Name, cpuCapacityQuantity, cpuUsageQuantity, memCapacityQuantity, memUsageQuantity, diskCapacityQuantity, diskUsageQuantity, diskEpheCapacityQuantity, diskEpheUsageQuantity)
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

	for nodeName, p := range mapOfNodePriorities {
		if p > maxP || bestNode == "" {
			maxP = p
			bestNode = nodeName
		}
	}

	return bestNode
}

//Deprecated: método herdado do "random-scheduler"
func randomPredicate(node *coreV1.Node, pod *coreV1.Pod) bool {
	// r := rand.Intn(2)

	return 0 == 0
}

//Deprecated: método herdado do "random-scheduler"
func randomPriority(node *coreV1.Node, pod *coreV1.Pod) int {
	// return rand.Intn(100)
	return 1
}

//realizando o "bind" do POD ao nó computacional
//neste ponto todas as estratégias já foram aplicadas
//- lista de NODES com afinidade de labels
//- priorização de NODES com maior capacidade ociosa
func (scheduler *Scheduler) bindPodOnNode(pod *coreV1.Pod, nodeName string) error {
	return scheduler.clientset.CoreV1().Pods(pod.Namespace).Bind(context.TODO(), &coreV1.Binding{
		ObjectMeta: metaV1.ObjectMeta{
			Name:      pod.Name,
			Namespace: pod.Namespace,
		},
		Target: coreV1.ObjectReference{
			APIVersion: "v1",
			Kind:       "Node",
			Name:       nodeName,
		},
	}, metaV1.CreateOptions{})
}
