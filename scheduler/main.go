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
	//inicialização do ambiente
	log.SetFlags(0)

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

			//verificando se o node possui algum TAINT ref. a NoSchedule, pois nestes casos, ele não poderá receber nenhum POD
			hasTaints := checkIfNodeHasTaints(node)

			if hasTaints {
				log.Printf("Node %s unavailable", node.GetName())

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
				//log no console ref. exclusão de Pod
				message := fmt.Sprintf("Pod [%s/%s] removed from Node %s", pod.Namespace, pod.Name, pod.Spec.NodeName)

				//gerando eventos no console do Kubernetes para acompanhar o custom scheduler
				err = scheduler.emitEvent(pod, "Unscheduled", message)

				if err != nil {
					log.Println("failed to emit unscheduled event", err.Error())

					return
				}

				if scheduler.debugAffinityEvents {
					log.Println(message)
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

	msgPodReceived := fmt.Sprintf("Received a POD to schedule: %s/%s", podQueue.Namespace, podQueue.Name)
	log.Println(msgPodReceived)

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
	message := fmt.Sprintf("Custom Scheduler [%s] assigned pod [%s/%s] to [%s]", scheduler.name, podQueue.Namespace, podQueue.Name, nodeForPodBind)

	//gerando eventos no console do Kubernetes para acompanhar o custom scheduler
	err = scheduler.emitEvent(podQueue, "Scheduled", message)

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
func (scheduler *Scheduler) buildMapOfNodesByLabelAffinity(listOfNodes []*coreV1.Node, pod *coreV1.Pod) map[*coreV1.Node]float64 {
	mapOfNodesByLabelAffinity := make(map[*coreV1.Node]float64)

	//definido qual o DNS definido p/ análise dos labels via prefixo
	dnsForLabelPrefix := scheduler.labelDNSPrefix

	//percorrendo as labels vinculadas ao POD
	if scheduler.debugAffinityEvents {
		log.Println("Pod labels for custom scheduler " + scheduler.name)
	}

	mapOfPodLabelsCustomSchedulerStrategy := make(map[string]string)

	for podLabelKey, podLabelValue := range pod.ObjectMeta.Labels {
		if !strings.HasPrefix(podLabelKey, dnsForLabelPrefix) {
			continue
		}

		//adicionando os labels do POD ao mapa de controle para realizar comparações de nós
		mapOfPodLabelsCustomSchedulerStrategy[podLabelKey] = podLabelValue

		if scheduler.debugAffinityEvents {
			log.Println("--> " + podLabelKey + ": " + podLabelValue)
		}
	}

	//verificando se não foi encontrado nenhum label para o POD
	if scheduler.debugAffinityEvents {
		if len(mapOfPodLabelsCustomSchedulerStrategy) == 0 {
			log.Println("<empty> no label definition with prefix " + dnsForLabelPrefix + " on current pod")
		}
	}

	for _, node := range listOfNodes {
		//verificando se o node possui algum TAINT ref. a NoSchedule, pois nestes casos, ele não poderá receber nenhum POD
		hasTaints := checkIfNodeHasTaints(node)

		if hasTaints {
			if scheduler.debugAffinityEvents {
				log.Println("Node " + node.Name + " tainted! Skipped")
			}

			continue
		}

		if scheduler.debugAffinityEvents {
			log.Println("Node " + node.Name + " labels for custom scheduler " + scheduler.name)
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
				log.Println("--> " + nodeLabelKey + ": " + nodeLabelValue)
			}
		}

		//verificando se não foi encontrado nenhum label para o NODE
		if scheduler.debugAffinityEvents {
			if len(mapOfNodeLabelsCustomSchedulerStrategy) == 0 {
				log.Println("<empty> no label definition with prefix " + dnsForLabelPrefix + " on current node")
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
		strAffinityValue := fmt.Sprintf("%f", affinityValue)
		log.Println("--> " + node.Name + " [" + strAffinityValue + "]")
	}

	return mapOfNodesByLabelAffinity
}

func computeLabelAffinityValue(mapOfPodLabelsCustomSchedulerStrategy map[string]string, mapOfNodeLabelsCustomSchedulerStrategy map[string]string) (float64, error) {
	affinityValue := 0.0
	var affinityError error

	//lista de operações validas
	arrOfAllowedOperators := []string{"eq", "ne", "gt", "ge", "lt", "le", "like", "notlike", "contains", "notcontains", "in", "notin"}

	//neste ponto é realizado o check de labels
	//verificar operador definido no valor do label dentro do POD
	//-- https://docs.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_comparison_operators?view=powershell-7.1
	//-- default será EQUAL
	for podLabelKey, podLabelValueRaw := range mapOfPodLabelsCustomSchedulerStrategy {
		//o padrão como operação será EQUAL e todo o conteudo da label como o valor a ser comparado
		podLabelOperator := "eq"
		podLabelValue := podLabelValueRaw
		podLabelOptional := false

		//verificando a composição de OPERACAO + VALOR
		podLabelValueArgs := strings.Split(podLabelValueRaw, "-")

		//se houverem duas posições após o split em "-", significa que encontrou um label com OPERACAO + VALOR
		if len(podLabelValueArgs) >= 2 {
			podLabelOperator = podLabelValueArgs[0]
			podLabelValue = strings.Join(podLabelValueArgs[1:], "-")
			podLabelOptional = strings.HasSuffix(podLabelOperator, "_")

			if podLabelOptional {
				podLabelOperator = strings.ReplaceAll(podLabelOperator, "_", "")
			}
		}

		//verificando se a operação esta contida na lista valida
		if !stringInSlice(podLabelOperator, arrOfAllowedOperators) {
			podLabelOperator = "eq"
			podLabelOptional = false
		}

		//variavel de apoio para saber se o nó computacional deverá ser "desprezado" por não atender os pré-requisitos do POD
		nodeNotMeetRequirements := false

		//percorrendo os labels do nó computacional
		for nodeLabelKey, nodeLabelValue := range mapOfNodeLabelsCustomSchedulerStrategy {
			if nodeLabelKey != podLabelKey {
				continue
			}

			var podLabelValueAsFloat float64 = 0.0
			var nodeLabelValueAsFloat float64 = 0.0
			var errPodLabelValue error
			var errNodeLabelValue error
			var mathLabelValueOperation bool = false

			//apoio para realizar calculos envolvendo operadores matemáticos, em que o valor de afinidade será calculado
			if stringInSlice(podLabelOperator, []string{"gt", "ge", "lt", "le"}) {
				podLabelValueAsFloat, errPodLabelValue = strconv.ParseFloat(podLabelValue, 10)
				nodeLabelValueAsFloat, errNodeLabelValue = strconv.ParseFloat(nodeLabelValue, 10)

				//se houver problema
				if errPodLabelValue == nil && errNodeLabelValue == nil {
					mathLabelValueOperation = true
				}
			}

			//compatibilização com a estratégia de MATCH e wildcards pois a sintaxe de valores possíveis para um label eh:
			//(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])?
			//neste contexto, o "_" irá representar um caracter qualquer
			//enquanto o "-" irá representar um conjunto indefinido de caracteres
			if stringInSlice(podLabelOperator, []string{"like", "notlike"}) {
				podLabelValue = strings.ReplaceAll(podLabelValue, "_", "?")
				podLabelValue = strings.ReplaceAll(podLabelValue, "-", "*")
			}

			switch podLabelOperator {
			case "eq":
				if nodeLabelValue == podLabelValue {
					affinityValue = 1
				} else if !podLabelOptional {
					nodeNotMeetRequirements = true
				}
			case "ne":
				if nodeLabelValue != podLabelValue {
					affinityValue = 1
				} else if !podLabelOptional {
					nodeNotMeetRequirements = true
				}
			case "gt":
				if mathLabelValueOperation && nodeLabelValueAsFloat > podLabelValueAsFloat {
					if podLabelValueAsFloat <= 0 {
						affinityValue = 1
					} else {
						affinityValue = nodeLabelValueAsFloat / podLabelValueAsFloat
					}
				} else if nodeLabelValue > podLabelValue {
					affinityValue = 1
				} else if !podLabelOptional {
					nodeNotMeetRequirements = true
				}
			case "ge":
				if mathLabelValueOperation && nodeLabelValueAsFloat >= podLabelValueAsFloat {
					if podLabelValueAsFloat <= 0 {
						affinityValue = 1
					} else {
						affinityValue = nodeLabelValueAsFloat / podLabelValueAsFloat
					}
				} else if nodeLabelValue >= podLabelValue {
					affinityValue = 1
				} else if !podLabelOptional {
					nodeNotMeetRequirements = true
				}
			case "lt":
				if mathLabelValueOperation && nodeLabelValueAsFloat < podLabelValueAsFloat {
					if nodeLabelValueAsFloat <= 0 {
						affinityValue = 1
					} else {
						affinityValue = (podLabelValueAsFloat / nodeLabelValueAsFloat) - 1
					}
				} else if nodeLabelValue < podLabelValue {
					affinityValue = 1
				} else if !podLabelOptional {
					nodeNotMeetRequirements = true
				}
			case "le":
				if mathLabelValueOperation && nodeLabelValueAsFloat <= podLabelValueAsFloat {
					if nodeLabelValueAsFloat <= 0 {
						affinityValue = 1
					} else {
						affinityValue = (podLabelValueAsFloat / nodeLabelValueAsFloat) - 1
					}
				} else if nodeLabelValue <= podLabelValue {
					affinityValue = 1
				} else if !podLabelOptional {
					nodeNotMeetRequirements = true
				}
			case "like":
				if wildcard.Match(podLabelValue, nodeLabelValue) {
					affinityValue = 1
				} else if !podLabelOptional {
					nodeNotMeetRequirements = true
				}
			case "notlike":
				if !wildcard.Match(podLabelValue, nodeLabelValue) {
					affinityValue = 1
				} else if !podLabelOptional {
					nodeNotMeetRequirements = true
				}
			case "contains":
				if strings.Contains(nodeLabelValue, podLabelValue) {
					affinityValue = 1
				} else if !podLabelOptional {
					nodeNotMeetRequirements = true
				}
			case "notcontains":
				if !strings.Contains(nodeLabelValue, podLabelValue) {
					affinityValue = 1
				} else if !podLabelOptional {
					nodeNotMeetRequirements = true
				}
			case "in":
				arrOfLabelValues := strings.Split(podLabelValue, "-")

				if stringInSlice(nodeLabelValue, arrOfLabelValues) {
					affinityValue = 1
				} else if !podLabelOptional {
					nodeNotMeetRequirements = true
				}
			case "notin":
				arrOfLabelValues := strings.Split(podLabelValue, "-")

				if !stringInSlice(nodeLabelValue, arrOfLabelValues) {
					affinityValue = 1
				} else if !podLabelOptional {
					nodeNotMeetRequirements = true
				}
			}
		}

		//se a variavel "hasAntiAffinityRestrictions" for TRUE, significa que valores minimos não foram atingidos, sendo necessário ignorar o nó em questão
		if nodeNotMeetRequirements {
			affinityError = errors.New("Node Labels doesn't meet requirements for Pod Labels")

			break
		}
	}

	return affinityValue, affinityError
}

//gerando eventos no console do Kubernetes para acompanhamento
func (scheduler *Scheduler) emitEvent(p *coreV1.Pod, reason string, message string) error {
	timestamp := time.Now().UTC()

	_, err := scheduler.clientset.CoreV1().Events(p.Namespace).Create(context.TODO(), &coreV1.Event{
		Count:          1,
		Message:        message,
		Reason:         reason,
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
func (scheduler *Scheduler) buildNodePriority(mapOfNodesByLabelAffinity map[*coreV1.Node]float64, pod *coreV1.Pod) map[string]float64 {
	configMetrics, err := rest.InClusterConfig()

	if err != nil {
		log.Fatal(err)
	}

	metricsConfig, err := metrics.NewForConfig(configMetrics)

	mapOfNodePriorities := make(map[string]float64)

	for node, affinityValue := range mapOfNodesByLabelAffinity {
		//obtendo as métricas de uso ref. ao NODE atual
		nodeMetrics, err := metricsConfig.MetricsV1beta1().NodeMetricses().Get(context.TODO(), node.Name, metaV1.GetOptions{})

		if err != nil {
			log.Fatal(err)
		}

		//por definição, um node esta limitado a 110 PODs
		//https://kubernetes.io/docs/setup/best-practices/cluster-large
		podCapacityValue := int64(110)

		//declarando atributos para calculo de consumo dos recursos computacionais do NODE atual
		cpuCapacityValue := node.Status.Allocatable.Cpu().MilliValue()
		memCapacityValue := node.Status.Allocatable.Memory().Value()
		cpuUsageValue := nodeMetrics.Usage.Cpu().MilliValue()
		memUsageValue := nodeMetrics.Usage.Memory().Value()
		podUsageValue := nodeMetrics.Usage.Pods().Value()

		msgNodeUsage := fmt.Sprintf("Node [%s] usage -> CPU [%d/%d] Memory [%d/%d] POD [%d/%d]", node.Name, cpuCapacityValue, cpuUsageValue, memCapacityValue, memUsageValue, podCapacityValue, podUsageValue)
		log.Println(msgNodeUsage)

		//a prioridade do nó computacional será definida mediante a afinidade com as labels definidas somada a capacidade livre disponível
		cpuCapacityUsage := float64(cpuUsageValue) / float64(cpuCapacityValue)
		memCapacityUsage := float64(memUsageValue) / float64(memCapacityValue)
		podCapacityUsage := float64(podUsageValue) / float64(podCapacityValue)

		//calculando a média de consumo entre CPU e MEMÓRIA
		computeCapacityIdle := (1 - (cpuCapacityUsage+memCapacityUsage+podCapacityUsage)/3) * 100

		//agregando ao valor de afinidade o percentual de capacidade livre
		nodePriority := (affinityValue + computeCapacityIdle)

		//se um NODE já chegou ao seu limite de PODs, forço para que ele não seja considerado
		if podUsageValue >= podCapacityValue {
			nodePriority = -1
		}

		//atualizado a prioridade do nó computacional com base em sua capacidade "idle"
		mapOfNodePriorities[node.Name] = nodePriority
	}

	if scheduler.debugAffinityEvents {
		log.Println("Nodes calculated priorities:")

		for nodeName, nodePriority := range mapOfNodePriorities {
			msgNodePriority := fmt.Sprintf("--> %s [%f]", nodeName, nodePriority)
			log.Println(msgNodePriority)
		}
	}

	return mapOfNodePriorities
}

//percorre o mapa de prioridades dos nodes em busca daquele mais "indicado" a receber a instância do node
func (scheduler *Scheduler) findBestNode(mapOfNodePriorities map[string]float64) string {
	var maxP float64
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

func checkIfNodeHasTaints(node *coreV1.Node) bool {
	arrOfTaints := node.Spec.Taints

	for _, taint := range arrOfTaints {
		if taint.Key == "node.kubernetes.io/unreachable" && taint.Value == "NoSchedule" {
			return true
		}
	}

	return false
}

//verificando se uma string esta contida em um array de valores
func stringInSlice(strParam string, arrOfStringValues []string) bool {
	for _, strArrValue := range arrOfStringValues {
		if strArrValue != strParam {
			continue
		}

		return true
	}

	return false
}
