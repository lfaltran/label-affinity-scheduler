package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"context"
	"errors"

	"github.com/minio/pkg/wildcard"

	// appsV1 "k8s.io/api/apps/v1"
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

//POJO de apoio para realizar a gestão de PODS e o processo de Bind
type Scheduler struct {
	name                string
	labelDNSPrefix      string
	debugAffinityEvents bool
	context             context.Context
	clientset           *kubernetes.Clientset
	podQueue            chan *coreV1.Pod
	nodeLister          listersV1.NodeLister
}

//inicio da execução, disponibilizando o Custom Scheduler como um Deployment
func main() {
	//obtendo as variáveis via Args da execução
	schedulerName := os.Args[1]
	labelDNSPrefix := os.Args[2]
	debugAffinityEvents, err := strconv.ParseBool(os.Args[3])

	if err != nil {
		debugAffinityEvents = false
	}

	msgWelcome := fmt.Sprintf("Custom Scheduler Ready! I'm [%s] watching labels [%s]", schedulerName, labelDNSPrefix)
	log.Println(msgWelcome)

	//criando nova instância do Custom Scheduler
	scheduler := Scheduler{
		name:                schedulerName,
		labelDNSPrefix:      labelDNSPrefix,
		debugAffinityEvents: debugAffinityEvents,
	}

	//variavel Channel que armazenará os PODS criados pelo orquestrador e irá enfileirar os eventos de BIND
	podQueue := make(chan *coreV1.Pod, 300)
	defer close(podQueue)

	quit := make(chan struct{})
	defer close(quit)

	//vinculando EventHandler dos Nodes/Pods ao scheduler
	buildSchedulerEventHandler(&scheduler, podQueue, quit)

	scheduler.run(quit)
}

//construção do objeto Scheduler, que será utilizado durante disparo dos eventos de schedule do Kubernetes
func buildSchedulerEventHandler(scheduler *Scheduler, podQueue chan *coreV1.Pod, quit chan struct{}) {
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

	// //encontrando o DEPLOYMENT que se refere ao Custom Scheduler atual
	// deploymentOfCustomScheduler, err := clientset.AppsV1().Deployments(coreV1.NamespaceDefault).Get(context, scheduler.name, metaV1.GetOptions{})

	// if err != nil {
	// 	log.Fatal(err)
	// }

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
			if pod.Spec.NodeName == "" && pod.Spec.SchedulerName == scheduler.name {
				podQueue <- pod
			}
		},
		DeleteFunc: func(obj interface{}) {
			pod, podOk := obj.(*coreV1.Pod)

			if !podOk {
				log.Println("this is not a pod")

				return
			}

			if pod.Spec.NodeName != "" && pod.Spec.SchedulerName == scheduler.name {
				if scheduler.debugAffinityEvents {
					log.Println("Pod " + pod.Namespace + "/" + pod.Name + " removed from Node " + pod.Spec.NodeName)
				}
			}
		},
	})

	factory.Start(quit)

	//ajustando atributos do Custom Scheduler
	scheduler.context = context
	scheduler.clientset = clientset
	scheduler.podQueue = podQueue
	scheduler.nodeLister = nodeLister
}

//método executado após interceptação de um novo POD
func (scheduler *Scheduler) run(quit chan struct{}) {
	wait.Until(scheduler.schedulePodInQueue, 0, quit)
}

//Realizando o "agendamento" do POD a um nó computacional
func (scheduler *Scheduler) schedulePodInQueue() {
	podQueue := <-scheduler.podQueue
	log.Println("Received a POD to schedule: ", podQueue.Namespace, "/", podQueue.Name)

	//encontrando o nó computacional com a maior afinidade baseado nas Labels definidas e priorizado pela maior capacidade computacional ociosa
	nodeForPodBind, err := scheduler.findNodeForPodBind(podQueue)

	if err != nil {
		log.Println("Cannot find node that fits pod", err.Error())

		return
	}

	//realizando o "bind" do POD ao nó computacional
	err = scheduler.bindPodOnNode(podQueue, nodeForPodBind)

	if err != nil {
		log.Println("failed to bind pod", err.Error())

		return
	}

	//log no console ref. ao bind realizado
	message := fmt.Sprintf("Placed pod [%s/%s] on %s", podQueue.Namespace, podQueue.Name, nodeForPodBind)

	//gerando eventos no console do Kubernetes para acompanhar o custom scheduler
	err = scheduler.emitEvent(podQueue, message)

	if err != nil {
		log.Println("failed to emit scheduled event", err.Error())

		return
	}

	log.Println(message)
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
	mapOfNodesByLabelAffinity := scheduler.buildMapOfNodesByLabelAffinity(listOfNodes, pod)

	//nenhum nó compativel :(
	if len(mapOfNodesByLabelAffinity) == 0 {
		return "", errors.New("Failed to find node that fits pod")
	}

	//aplicando estratégia de rankear os nodes de acordo com sua capacidade ociosa
	mapOfNodePriorities := scheduler.buildNodePriority(mapOfNodesByLabelAffinity, pod)

	//percorrendo o mapa de prioridades para escolher o nó com maior capacidade ociosa, dentre aqueles que já passaram pelas regras de afinidade das labels
	nodeForPodBind := scheduler.findBestNode(mapOfNodePriorities)

	return nodeForPodBind, nil
}

//listando os nós computacionais de acordo com a afinidade de labels entre PODS e NODES
func (scheduler *Scheduler) buildMapOfNodesByLabelAffinity(listOfNodes []*coreV1.Node, pod *coreV1.Pod) map[*coreV1.Node]int {
	mapOfNodesByLabelAffinity := make(map[*coreV1.Node]int)

	//definido qual o DNS definido p/ análise dos labels via prefixo
	dnsForLabelPrefix := scheduler.labelDNSPrefix

	//percorrendo as labels vinculadas ao POD
	if scheduler.debugAffinityEvents {
		log.Println("Pod labels for context " + scheduler.name)
	}

	mapOfPodLabelsCustomSchedulerStrategy := make(map[string]string)

	for podLabelKey, podLabelValue := range pod.ObjectMeta.Labels {
		if !strings.HasPrefix(podLabelKey, dnsForLabelPrefix) {
			continue
		}

		//adicionando os labels do POD ao mapa de controle para realizar comparações de nós
		mapOfPodLabelsCustomSchedulerStrategy[podLabelKey] = podLabelValue

		if scheduler.debugAffinityEvents {
			log.Println(podLabelKey + ": " + podLabelValue)
		}
	}

	for _, node := range listOfNodes {
		if scheduler.debugAffinityEvents {
			log.Println("Node " + node.Name + " labels for context " + scheduler.name)
		}

		mapOfNodeLabelsCustomSchedulerStrategy := make(map[string]string)

		//percorrendo as labels vinculadas ao NODE
		for nodeLabelKey, nodeLabelValue := range node.ObjectMeta.Labels {
			if !strings.HasPrefix(nodeLabelKey, dnsForLabelPrefix) {
				continue
			}

			//adicionando os labels do Node ao mapa de controle
			mapOfNodeLabelsCustomSchedulerStrategy[nodeLabelKey] = nodeLabelValue

			if scheduler.debugAffinityEvents {
				log.Println(nodeLabelKey + ": " + nodeLabelValue)
			}
		}

		//realizando o calculo de afinidade das labels entre Pod e Node
		//valores NEGATIVOS significando que algum label obrigatório não foi atendido
		affinityValue, err := computeLabelAffinityValue(mapOfPodLabelsCustomSchedulerStrategy, mapOfNodeLabelsCustomSchedulerStrategy)

		if err != nil {
			if scheduler.debugAffinityEvents {
				log.Println("Node " + node.Name + " not suitable for Pod " + pod.Namespace + "/" + pod.Name + " => " + err.Error())
			}

			continue
		}

		mapOfNodesByLabelAffinity[node] = affinityValue
	}

	log.Println("Nodes that fit:")

	for node, affinityValue := range mapOfNodesByLabelAffinity {
		log.Println(node.Name + " [" + strconv.Itoa(affinityValue) + "]")
	}

	return mapOfNodesByLabelAffinity
}

func computeLabelAffinityValue(mapOfPodLabelsCustomSchedulerStrategy map[string]string, mapOfNodeLabelsCustomSchedulerStrategy map[string]string) (int, error) {
	affinityValue := 0
	var affinityError error

	//lista de operações validas
	mapOfAllowedOperators := map[string]struct{}{"eq": {}, "ne": {}, "gt": {}, "ge": {}, "lt": {}, "le": {}, "like": {}, "notlike": {}, "contains": {}, "notcontains": {}, "in": {}, "notin": {}}

	//neste ponto é realizado o check de labels
	//verificar operador definido no valor do label dentro do POD
	//-- https://docs.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_comparison_operators?view=powershell-7.1
	//-- default será EQUAL
	for podLabelKey, podLabelValue := range mapOfPodLabelsCustomSchedulerStrategy {
		//o padrão como operação será EQUAL e todo o conteudo da label como o valor a ser comparado
		labelOperator := "eq"
		labelValue := podLabelValue

		//verificando a composição de OPERACAO + VALOR
		labelValueArgs := strings.Split(podLabelValue, "-")

		//se houverem duas posições após o split em "-", significa que encontrou um label com OPERACAO + VALOR
		if len(labelValueArgs) >= 2 {
			labelOperator = labelValueArgs[0]
			labelValue = strings.Join(labelValueArgs[1:], "-")
		}

		//verificando se a operação esta contida na lista valida
		if _, operatorAllowed := mapOfAllowedOperators[labelOperator]; !operatorAllowed {
			labelOperator = "eq"
		}

		//percorrendo os labels do nó computacional
		for nodeLabelKey, nodeLabelValue := range mapOfNodeLabelsCustomSchedulerStrategy {
			if nodeLabelKey != podLabelKey {
				continue
			}

			switch labelOperator {
			case "eq":
				if nodeLabelValue != labelValue {
					affinityValue = -1
				}
			case "ne":
				if nodeLabelValue == labelValue {
					affinityValue = -1
				}
			case "gt":
				if nodeLabelValue <= labelValue {
					affinityValue = -1
				}
			case "ge":
				if nodeLabelValue < labelValue {
					affinityValue = -1
				}
			case "lt":
				if nodeLabelValue >= labelValue {
					affinityValue = -1
				}
			case "le":
				if nodeLabelValue > labelValue {
					affinityValue = -1
				}
			case "like":
				//implementação experimental, pois a sintaxe de valores possíveis para um label eh:
				//(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])?
				//neste caso, não é possível utilizar "*" e "?" para construir os wildcards
				if !wildcard.Match(labelValue, nodeLabelValue) {
					affinityValue = -1
				}
			case "notlike":
				//implementação experimental, pois a sintaxe de valores possíveis para um label eh:
				//(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])?
				//neste caso, não é possível utilizar "*" e "?" para construir os wildcards
				if wildcard.Match(labelValue, nodeLabelValue) {
					affinityValue = -1
				}
			case "contains":
				if !strings.Contains(nodeLabelValue, labelValue) {
					affinityValue = -1
				}
			case "notcontains":
				if strings.Contains(nodeLabelValue, labelValue) {
					affinityValue = -1
				}
			case "in":
				arrOfLabelValues := strings.Split(labelValue, "-")

				if !stringInSlice(nodeLabelValue, arrOfLabelValues) {
					affinityValue = -1
				}
			case "notin":
				arrOfLabelValues := strings.Split(labelValue, "-")

				if stringInSlice(nodeLabelValue, arrOfLabelValues) {
					affinityValue = -1
				}
			}
		}

		//se o "affinityValue" for negativo, significa que valores minimos não foram atingidos, sendo necessário ignorar o nó em questão
		if affinityValue < 0 {
			affinityError = errors.New("Node Labels doesn't meet requirements for Pod Labels")

			break
		}
	}

	return affinityValue, affinityError
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
			Component: scheduler.name,
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
func (scheduler *Scheduler) buildNodePriority(mapOfNodesByLabelAffinity map[*coreV1.Node]int, pod *coreV1.Pod) map[string]int {
	configMetrics, err := rest.InClusterConfig()

	if err != nil {
		log.Fatal(err)
	}

	metricsConfig, err := metrics.NewForConfig(configMetrics)

	mapOfNodePriorities := make(map[string]int)

	for node, affinityValue := range mapOfNodesByLabelAffinity {
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

		msgNodeUsage := fmt.Sprintf("Node [%s] usage -> CPU [%d/%d] Memory [%d/%d] Disk [%d/%d] Disk Ephemeral [%d/%d]", node.Name, cpuCapacityQuantity, cpuUsageQuantity, memCapacityQuantity, memUsageQuantity, diskCapacityQuantity, diskUsageQuantity, diskEpheCapacityQuantity, diskEpheUsageQuantity)
		log.Println(msgNodeUsage)

		//a prioridade do nó computacional será definida mediante a afinidade com as labels definidas somada a capacidade livre disponível
		nodePriority := (affinityValue + int(cpuUsageQuantity))

		mapOfNodePriorities[node.Name] = nodePriority

		//codigo obsoleto
		// for _, priority := range scheduler.priorities {
		// 	mapOfNodePriorities[node.Name] += priority(node, pod)
		// }
	}

	if scheduler.debugAffinityEvents {
		log.Println("Calculated priorities:")
		log.Println(mapOfNodePriorities)
	}

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

func stringInSlice(a string, list []string) bool {
	for _, b := range list {
		if b == a {
			return true
		}
	}
	return false
}
