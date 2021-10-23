package main

import (
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"context"
	"errors"

	"github.com/minio/pkg/wildcard"

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

	// kubestatemetrics "k8s.io/kube-state-metrics/v2"
	metricsserver "k8s.io/metrics/pkg/client/clientset/versioned"
)

//POJO de apoio para realizar a gestão de PODS e o processo de Bind
type Scheduler struct {
	name                string
	labelDNSPrefix      string
	metricsStrategy     string
	debugAffinityEvents bool
	context             context.Context
	clientset           *kubernetes.Clientset
	nodeLister          listersV1.NodeLister
	podQueued           chan *coreV1.Pod
}

type NodePodDistribution struct {
	node     *coreV1.Node
	podCount int
}
type NodeLabelAffinity struct {
	node          *coreV1.Node
	affinityValue float64
}

type NodePriority struct {
	node          *coreV1.Node
	priorityValue float64
}

//inicio da execução, disponibilizando o Custom Scheduler como um Deployment
func main() {
	//inicialização do ambiente
	log.SetFlags(0)

	//obtendo as variáveis via Args da execução
	schedulerName := os.Args[1]
	labelDNSPrefix := os.Args[2]
	metricsStrategy := os.Args[3]

	debugAffinityEvents, err := strconv.ParseBool(os.Args[4])

	if err != nil {
		debugAffinityEvents = false
	}

	msgWelcome := fmt.Sprintf("Custom Scheduler Ready! I'm [%s] watching labels [%s] metrics provided by [%s]", schedulerName, labelDNSPrefix, metricsStrategy)
	log.Println(msgWelcome)

	//criando nova instância do Custom Scheduler
	scheduler := Scheduler{
		name:                schedulerName,
		labelDNSPrefix:      labelDNSPrefix,
		metricsStrategy:     metricsStrategy,
		debugAffinityEvents: debugAffinityEvents,
	}

	//variavel Channel que armazenará os PODS criados pelo orquestrador e irá enfileirar os eventos de BIND
	podQueued := make(chan *coreV1.Pod, 300)
	defer close(podQueued)

	quit := make(chan struct{})
	defer close(quit)

	//vinculando EventHandler dos Nodes/Pods ao scheduler
	buildSchedulerEventHandler(&scheduler, podQueued, quit)

	scheduler.run(quit)
}

//construção do objeto Scheduler, que será utilizado durante disparo dos eventos de schedule do Kubernetes
func buildSchedulerEventHandler(scheduler *Scheduler, podQueued chan *coreV1.Pod, quit chan struct{}) {
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
	nodeCore := factory.Core().V1().Nodes()
	nodeCore.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			node, nodeOk := obj.(*coreV1.Node)

			if !nodeOk {
				log.Println("this is not a Node!")

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

	nodeLister := nodeCore.Lister()

	//adicionando evento p/ monitorar os PODS
	podCore := factory.Core().V1().Pods()
	podCore.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			pod, podOk := obj.(*coreV1.Pod)

			if !podOk {
				log.Println("this is not a Pod!")

				return
			}

			//verificando se o POD esta sem nenhum NODE vinculado e se o mesmo esta atrelado ao Custom Scheduler atual
			if pod.Spec.NodeName == "" && pod.Spec.SchedulerName == scheduler.name {
				podQueued <- pod
			}
		},
		DeleteFunc: func(obj interface{}) {
			pod, podOk := obj.(*coreV1.Pod)

			if !podOk {
				log.Println("this is not a Pod!")

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

				log.Println(message)
			}
		},
	})

	//adicionando lister p/ monitoramento dos deployments
	deploymentApp := factory.Apps().V1().Deployments()
	deploymentApp.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(oldObj interface{}, newObj interface{}) {
			deployment, deploymentOk := newObj.(*appsV1.Deployment)

			if !deploymentOk {
				log.Println("this is not a Deployment!")

				return
			}

			//verificando se existe o label de monitoramento do status de escalonamento
			_, hasDeploymentLogEnabled := deployment.Spec.Template.Annotations["lfaltran.io/deployment.node.log"]

			if !hasDeploymentLogEnabled {
				return
			}

			//se houver a annotation, verifico se o debug esta ativo
			deploymentLogEnabled, err := strconv.ParseBool(deployment.Spec.Template.Annotations["lfaltran.io/deployment.node.log"])

			if !deploymentLogEnabled || err != nil {
				return
			}

			//a partir deste ponto vou iniciar o monitoramento do deployment
			deploymentStatus := deployment.Status

			//verificando se o escalonamento já foi concluido
			if deploymentStatus.UnavailableReplicas > 0 || deploymentStatus.Replicas != deploymentStatus.ReadyReplicas {
				return
			}

			//percorrendo as condições do Deployment p/ saber se o registro do tipo "Available" esta "true"
			deploymentUnavailable := false
			arrOfDeploymentConditions := deployment.Status.Conditions

			for _, deploymentCondition := range arrOfDeploymentConditions {
				if deploymentCondition.Type != "Available" {
					continue
				}

				deploymentUnavailable = (deploymentCondition.Status != "True")
			}

			//neste ponto, identifico se o Deployment esta indisponível
			if deploymentUnavailable {
				return
			}

			//a partir daqui o Deployment esta supostamente com as atualizações finalizadas
			go scheduler.printDeploymentNodeAllocation(deployment, nodeLister)
		},
	})

	factory.Start(quit)

	//ajustando atributos do Custom Scheduler
	scheduler.context = context
	scheduler.clientset = clientset
	scheduler.nodeLister = nodeLister
	scheduler.podQueued = podQueued
}

//método executado após interceptação de um novo POD
func (scheduler *Scheduler) run(quit chan struct{}) {
	wait.Until(scheduler.schedulePodInQueue, 0, quit)
}

//Realizando o "agendamento" do POD a um nó computacional
func (scheduler *Scheduler) schedulePodInQueue() {
	podQueued := <-scheduler.podQueued

	msgPodReceived := fmt.Sprintf("Received a POD to schedule: %s/%s", podQueued.Namespace, podQueued.Name)
	log.Println(msgPodReceived)

	//encontrando o nó computacional com a maior afinidade baseado nas Labels definidas e priorizado pela maior capacidade computacional ociosa
	nodeForPodBind, err := scheduler.findNodeForPodBind(podQueued)

	if err != nil {
		log.Println("Cannot find node that fits POD", err.Error())

		return
	}

	//realizando o "bind" do POD ao nó computacional
	err = scheduler.bindPodOnNode(podQueued, nodeForPodBind)

	if err != nil {
		log.Println("failed to bind POD", err.Error())

		return
	}

	//log no console ref. ao bind realizado
	message := fmt.Sprintf("Custom Scheduler [%s] assigned POD [%s/%s] to [%s]", scheduler.name, podQueued.Namespace, podQueued.Name, nodeForPodBind.Name)

	//gerando eventos no console do Kubernetes para acompanhar o custom scheduler
	err = scheduler.emitEvent(podQueued, "Scheduled", message)

	if err != nil {
		log.Println("failed to emit scheduled event", err.Error())

		return
	}

	log.Println(message)
}

//Encontrando o nó computacional participante do cluster que possui a maior afinidade com o POD recebido
//obs.: Em caso de mais de um registro, será priorizado aquele com a maior capacidade computacional ociosa
func (scheduler *Scheduler) findNodeForPodBind(pod *coreV1.Pod) (*coreV1.Node, error) {
	//obtendo a lista de nodes (a partir do listener) disponíveis
	listOfNodes, err := scheduler.nodeLister.List(labels.Everything())

	if err != nil {
		return nil, err
	}

	//listando os nós computacionais a partir da afinidade de labels
	mapOfNodesByLabelAffinity := scheduler.buildMapOfNodesByLabelAffinity(listOfNodes, pod)

	//nenhum nó compativel :(
	if len(mapOfNodesByLabelAffinity) == 0 {
		return nil, errors.New("Failed to find node that fits pod")
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

	//iniciando debug dos Nodes que atendem a distribuição do POD
	if scheduler.debugAffinityEvents {
		log.Println("Nodes that fit:")

		//aplicando ordenação dos Nodes a partir de seu nome
		var arrOfNodeLabelAffinities []NodeLabelAffinity

		for k, v := range mapOfNodesByLabelAffinity {
			arrOfNodeLabelAffinities = append(arrOfNodeLabelAffinities, NodeLabelAffinity{k, v})
		}

		sort.Slice(arrOfNodeLabelAffinities, func(i, j int) bool {
			return arrOfNodeLabelAffinities[i].node.Name < arrOfNodeLabelAffinities[j].node.Name
		})

		for _, nodeLabelAffinity := range arrOfNodeLabelAffinities {
			node := nodeLabelAffinity.node
			affinityValue := nodeLabelAffinity.affinityValue
			strAffinityValue := fmt.Sprintf("%f", affinityValue)

			log.Println("--> " + node.Name + " [" + strAffinityValue + "]")
		}
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
		podLabelAffinityWeight := 1.0
		podLabelAffinityValue := 0.0

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

		//se o valor informado para o label possuir um ".", considero que o valor subsequente como sendo um "peso" no calculo do "affinityValue"
		podLabelWeightArgs := strings.Split(podLabelValue, ".")

		if len(podLabelWeightArgs) >= 2 {
			podLabelValue = podLabelWeightArgs[0]

			var errPodLabelAffinityWeight error
			podLabelAffinityWeight, errPodLabelAffinityWeight = strconv.ParseFloat(podLabelWeightArgs[1], 10)

			if errPodLabelAffinityWeight != nil {
				podLabelAffinityWeight = 1.0
			}
		}

		//verificando se a operação esta contida na lista valida
		if !stringInSlice(podLabelOperator, arrOfAllowedOperators) {
			podLabelOperator = "eq"
			podLabelOptional = false
		}

		//variavel de apoio para saber se o nó computacional deverá ser "desprezado" por não atender os pré-requisitos do POD
		nodeHasPodLabel := false
		nodeNotMeetRequirements := false

		//percorrendo os labels do nó computacional
		for nodeLabelKey, nodeLabelValue := range mapOfNodeLabelsCustomSchedulerStrategy {
			if nodeLabelKey != podLabelKey {
				continue
			}

			//a partir daqui o nó possui a definição do label alvo da verificação de afinidade
			nodeHasPodLabel = true

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
					podLabelAffinityValue = 1
				} else if !podLabelOptional {
					nodeNotMeetRequirements = true
				}
			case "ne":
				if nodeLabelValue != podLabelValue {
					podLabelAffinityValue = 1
				} else if !podLabelOptional {
					nodeNotMeetRequirements = true
				}
			case "gt":
				if mathLabelValueOperation && nodeLabelValueAsFloat > podLabelValueAsFloat {
					if podLabelValueAsFloat <= 0 {
						podLabelAffinityValue = 1
					} else {
						podLabelAffinityValue = nodeLabelValueAsFloat / podLabelValueAsFloat
					}
				} else if nodeLabelValue > podLabelValue {
					podLabelAffinityValue = 1
				} else if !podLabelOptional {
					nodeNotMeetRequirements = true
				}
			case "ge":
				if mathLabelValueOperation && nodeLabelValueAsFloat >= podLabelValueAsFloat {
					if podLabelValueAsFloat <= 0 {
						podLabelAffinityValue = 1
					} else {
						podLabelAffinityValue = nodeLabelValueAsFloat / podLabelValueAsFloat
					}
				} else if nodeLabelValue >= podLabelValue {
					podLabelAffinityValue = 1
				} else if !podLabelOptional {
					nodeNotMeetRequirements = true
				}
			case "lt":
				if mathLabelValueOperation && nodeLabelValueAsFloat < podLabelValueAsFloat {
					if nodeLabelValueAsFloat <= 0 {
						podLabelAffinityValue = 1
					} else {
						podLabelAffinityValue = (podLabelValueAsFloat / nodeLabelValueAsFloat) - 1
					}
				} else if nodeLabelValue < podLabelValue {
					podLabelAffinityValue = 1
				} else if !podLabelOptional {
					nodeNotMeetRequirements = true
				}
			case "le":
				if mathLabelValueOperation && nodeLabelValueAsFloat <= podLabelValueAsFloat {
					if nodeLabelValueAsFloat <= 0 {
						podLabelAffinityValue = 1
					} else {
						podLabelAffinityValue = (podLabelValueAsFloat / nodeLabelValueAsFloat) - 1
					}
				} else if nodeLabelValue <= podLabelValue {
					podLabelAffinityValue = 1
				} else if !podLabelOptional {
					nodeNotMeetRequirements = true
				}
			case "like":
				if wildcard.Match(podLabelValue, nodeLabelValue) {
					podLabelAffinityValue = 1
				} else if !podLabelOptional {
					nodeNotMeetRequirements = true
				}
			case "notlike":
				if !wildcard.Match(podLabelValue, nodeLabelValue) {
					podLabelAffinityValue = 1
				} else if !podLabelOptional {
					nodeNotMeetRequirements = true
				}
			case "contains":
				if strings.Contains(nodeLabelValue, podLabelValue) {
					podLabelAffinityValue = 1
				} else if !podLabelOptional {
					nodeNotMeetRequirements = true
				}
			case "notcontains":
				if !strings.Contains(nodeLabelValue, podLabelValue) {
					podLabelAffinityValue = 1
				} else if !podLabelOptional {
					nodeNotMeetRequirements = true
				}
			case "in":
				arrOfLabelValues := strings.Split(podLabelValue, "-")

				if stringInSlice(nodeLabelValue, arrOfLabelValues) {
					podLabelAffinityValue = 1
				} else if !podLabelOptional {
					nodeNotMeetRequirements = true
				}
			case "notin":
				arrOfLabelValues := strings.Split(podLabelValue, "-")

				if !stringInSlice(nodeLabelValue, arrOfLabelValues) {
					podLabelAffinityValue = 1
				} else if !podLabelOptional {
					nodeNotMeetRequirements = true
				}
			}

			//se houver afinidade, agrego a variavel que totaliza a pontuação geral do nó computacional
			if podLabelAffinityValue > 0 {
				affinityValue += (podLabelAffinityValue * podLabelAffinityWeight)
			}
		}

		//se a variavel "hasAntiAffinityRestrictions" for TRUE, significa que valores minimos não foram atingidos, sendo necessário ignorar o nó em questão
		if nodeNotMeetRequirements || (!podLabelOptional && !nodeHasPodLabel) {
			affinityError = errors.New("Node Labels doesn't meet requirements for Pod Labels")

			break
		}
	}

	return affinityValue, affinityError
}

//construindo a prioridade dos NODES a partir de sua capacidade computacional ociosa
func (scheduler *Scheduler) buildNodePriority(mapOfNodesByLabelAffinity map[*coreV1.Node]float64, pod *coreV1.Pod) map[*coreV1.Node]float64 {
	configMetrics, err := rest.InClusterConfig()

	if err != nil {
		log.Fatal(err)
	}

	if scheduler.debugAffinityEvents {
		log.Println(fmt.Sprintf("Metrics Provided By => %s", scheduler.metricsStrategy))
	}

	mapOfNodePriorities := make(map[*coreV1.Node]float64)

	for node, affinityValue := range mapOfNodesByLabelAffinity {
		//declarando atributos para calculo de consumo dos recursos computacionais do NODE atual
		cpuCapacityValue := node.Status.Allocatable.Cpu().MilliValue()
		memCapacityValue := node.Status.Allocatable.Memory().Value()
		podCapacityValue := node.Status.Allocatable.Pods().Value()

		//variáveis que armazenam o consumo computacional atual dos nós
		cpuUsageValue := int64(0)
		memUsageValue := int64(0)
		podUsageValue := int64(0)

		//calculando a quantidade de PODs em execução no nó atual
		podListFromCurrentNode, err := scheduler.clientset.CoreV1().Pods(metaV1.NamespaceAll).List(context.TODO(), metaV1.ListOptions{
			FieldSelector: "spec.nodeName=" + node.Name,
		})

		if err != nil {
			log.Println(fmt.Sprintf("[%s] Skip Metrics Priority! Error on PodList -> %s", node.Name, err))

			continue
		}

		//o calculo da quantidade de PODs em uso será realizado a partir do tamanho da lista encontrada pela busca de PODs vinculados ao nó
		podUsageValue = int64(len(podListFromCurrentNode.Items))

		//TODO buscar método alternativo p/ calculo da ociosidade computacional, estou tendo mtos problemas com o Metrics Server
		//obtendo as métricas de uso ref. ao NODE atual
		if scheduler.metricsStrategy == "metrics-server" {
			metricsServerConfig, err := metricsserver.NewForConfig(configMetrics)

			//utilizando a API "MetricsServer"
			nodeMetrics, err := metricsServerConfig.MetricsV1beta1().NodeMetricses().Get(context.TODO(), node.Name, metaV1.GetOptions{})

			//problema p/ obter dados via API de métricas
			if err != nil {
				//se não for possível calcular as métricas de um NODE, apenas adiciono ele na lista de prioridades com valor 0
				mapOfNodePriorities[node] = 0

				log.Println(fmt.Sprintf("[%s] Skip Metrics Priority! Error on NodeMetricses of Metrics Server -> %s", node.Name, err))

				continue
			}

			cpuUsageValue = nodeMetrics.Usage.Cpu().MilliValue()
			memUsageValue = nodeMetrics.Usage.Memory().Value()
			// podUsageValue = nodeMetrics.Usage.Pods().Value()
			// } else if scheduler.metricsStrategy == "kube-state-metrics" {
			// 	//utilizando método alternativo
			// 	kubeStateMetricsConfig, err := kubestatemetrics.NewForConfig(configMetrics)

			// 	//problema p/ obter dados via API de métricas
			// 	if err != nil {
			// 		//se não for possível calcular as métricas de um NODE, apenas adiciono ele na lista de prioridades com valor 0
			// 		mapOfNodePriorities[node] = 0

			// 		log.Println(fmt.Sprintf("[%s] Skip Metrics Priority! Error on NodeMetricses of Kube State Metrics -> %s", node.Name, err))

			// 		continue
			// 	}

			// 	v, err := kubeStateMetricsConfig.Discovery().ServerVersion()

			// 	if err != nil {
			// 		log.Println("error while trying to communicate with apiserver")

			// 		continue
			// 	}

			// 	log.Println(fmt.Sprintf("Running with Kubernetes cluster version: v%s.%s. git version: %s. git tree state: %s. commit: %s. platform: %s",
			// 		v.Major, v.Minor, v.GitVersion, v.GitTreeState, v.GitCommit, v.Platform))

			// 	log.Println("Communication with server successful")
		} else {
			//TODO calculando recurso utilizado pelos PODs diretamente dos limites definidos do deployment
			for _, podFromCurrentNode := range (*podListFromCurrentNode).Items {
				arrOfContainersFromPod := podFromCurrentNode.Spec.Containers

				for _, containerFromPod := range arrOfContainersFromPod {
					cpuUsageValue += containerFromPod.Resources.Limits.Cpu().MilliValue()
					memUsageValue += containerFromPod.Resources.Limits.Memory().Value()
				}
			}
		}

		if scheduler.debugAffinityEvents {
			msgNodeUsage := fmt.Sprintf("Node [%s] usage -> CPU [%d/%d] Memory [%d/%d] POD [%d/%d]", node.Name, cpuCapacityValue, cpuUsageValue, memCapacityValue, memUsageValue, podCapacityValue, podUsageValue)

			log.Println(msgNodeUsage)
		}

		//a prioridade do nó computacional será definida mediante a afinidade com as labels definidas somada a capacidade livre disponível
		cpuCapacityUsage := float64(cpuUsageValue) / float64(cpuCapacityValue)
		memCapacityUsage := float64(memUsageValue) / float64(memCapacityValue)
		podCapacityUsage := float64(podUsageValue) / float64(podCapacityValue)

		//calculando a média de consumo entre CPU e MEMÓRIA
		computeCapacityIdle := (1 - (cpuCapacityUsage+memCapacityUsage)/2) * 100

		var nodePriority float64 = 0

		//se um NODE já chegou ao seu limite de PODs, forço para que ele não seja considerado
		if podUsageValue >= podCapacityValue {
			nodePriority = -1
		} else {
			//calculando a capacidade livre de POD
			podCapacityIdle := (1 - podCapacityUsage*100)

			//agregando ao valor de afinidade o percentual de capacidade livre
			nodePriority = (affinityValue + computeCapacityIdle + podCapacityIdle)
		}

		//atualizado a prioridade do nó computacional com base em sua capacidade "idle"
		mapOfNodePriorities[node] = nodePriority
	}

	if scheduler.debugAffinityEvents {
		//iniciando debug dos Nodes que atendem a distribuição do POD
		log.Println("Nodes calculated priorities:")

		//aplicando ordenação dos Nodes a partir da pontuação de afinidade
		var arrOfNodePriorities []NodePriority

		for k, v := range mapOfNodePriorities {
			arrOfNodePriorities = append(arrOfNodePriorities, NodePriority{k, v})
		}

		sort.Slice(arrOfNodePriorities, func(i, j int) bool {
			return arrOfNodePriorities[i].priorityValue > arrOfNodePriorities[j].priorityValue
		})

		for _, nodePriority := range arrOfNodePriorities {
			msgNodePriority := fmt.Sprintf("--> %s [%f]", nodePriority.node.Name, nodePriority.priorityValue)

			log.Println(msgNodePriority)
		}
	}

	return mapOfNodePriorities
}

//percorre o mapa de prioridades dos nodes em busca daquele mais "indicado" a receber a instância do node
func (scheduler *Scheduler) findBestNode(mapOfNodePriorities map[*coreV1.Node]float64) *coreV1.Node {
	var maxP float64
	var bestNode *coreV1.Node

	for node, p := range mapOfNodePriorities {
		if p > maxP || bestNode == nil {
			maxP = p
			bestNode = node
		}
	}

	return bestNode
}

//realizando o "bind" do POD ao nó computacional
//neste ponto todas as estratégias já foram aplicadas
//- lista de NODES com afinidade de labels
//- priorização de NODES com maior capacidade ociosa
func (scheduler *Scheduler) bindPodOnNode(pod *coreV1.Pod, node *coreV1.Node) error {
	return scheduler.clientset.CoreV1().Pods(pod.Namespace).Bind(context.TODO(), &coreV1.Binding{
		ObjectMeta: metaV1.ObjectMeta{
			Name:      pod.Name,
			Namespace: pod.Namespace,
		},
		Target: coreV1.ObjectReference{
			APIVersion: "v1",
			Kind:       "Node",
			Name:       node.Name,
		},
	}, metaV1.CreateOptions{})
}

func checkIfNodeHasTaints(node *coreV1.Node) bool {
	arrOfTaints := node.Spec.Taints

	if len(arrOfTaints) > 0 {
		return true
	}

	return false
}

//gerando eventos no console do Kubernetes para acompanhamento
func (scheduler *Scheduler) emitEvent(pod *coreV1.Pod, reason string, message string) error {
	timestamp := time.Now().UTC()

	_, err := scheduler.clientset.CoreV1().Events(pod.Namespace).Create(context.TODO(), &coreV1.Event{
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
			Name:      pod.Name,
			Namespace: pod.Namespace,
			UID:       pod.UID,
		},
		ObjectMeta: metaV1.ObjectMeta{
			GenerateName: pod.Name + "-",
		},
	}, metaV1.CreateOptions{})

	if err != nil {
		return err
	}

	return nil
}

func (scheduler *Scheduler) printDeploymentNodeAllocation(deployment *appsV1.Deployment, nodeLister listersV1.NodeLister) {
	//listar todos os Nodes
	listOfNodes, err := nodeLister.List(labels.Everything())

	if err != nil {
		return
	}

	mapOfNodesWithPodCount := make(map[*coreV1.Node]int)

	for _, node := range listOfNodes {
		mapOfNodesWithPodCount[node] = 0
	}

	//listar todos os Pods
	listOptionsForPodFilter := metaV1.ListOptions{
		LabelSelector: "app=" + deployment.Name,
	}

	//adicionando um tempo p/ que o status atual do deployment seja atualizado e a coleta de dados aconteça de forma mais precisa
	time.Sleep(10 * time.Second)

	podListFromCurrentDeployment, err := scheduler.clientset.CoreV1().Pods(deployment.Namespace).List(context.TODO(), listOptionsForPodFilter)

	if err != nil {
		return
	}

	//verificando se o "update" recebido esta de acordo com a quantidade de Pods atuais do deployment
	deploymentStatus := deployment.Status
	deploymentPodCount := int32(len(podListFromCurrentDeployment.Items))

	if deploymentStatus.Replicas != deploymentPodCount {
		return
	}

	//percorrendo os Pods e realizando calculo da distribuição nos Nodes
	for _, podFromCurrentDeployment := range (*podListFromCurrentDeployment).Items {
		for node := range mapOfNodesWithPodCount {
			if node.Name != podFromCurrentDeployment.Spec.NodeName {
				continue
			}

			mapOfNodesWithPodCount[node] += 1
		}
	}

	//listando os nodes e a quantidade de Pods atribuida a cada um deles
	var arrOfNodePodDistribution []NodePodDistribution

	for k, v := range mapOfNodesWithPodCount {
		arrOfNodePodDistribution = append(arrOfNodePodDistribution, NodePodDistribution{k, v})
	}

	sort.Slice(arrOfNodePodDistribution, func(i, j int) bool {
		return arrOfNodePodDistribution[i].node.Name < arrOfNodePodDistribution[j].node.Name
	})

	//a partir daqui o deployment esta concluido, então realizo e análise de distribuição dos Pods através dos Nodes
	log.Println(fmt.Sprintf("Deployment [%s/%s] scaled up to %d replica(s)", deployment.Namespace, deployment.Name, deploymentStatus.ReadyReplicas))

	for _, nodePodDistribution := range arrOfNodePodDistribution {
		node := nodePodDistribution.node
		podLimitOnNode := int(nodePodDistribution.node.Status.Allocatable.Pods().Value())
		podAllocatedOnNode := nodePodDistribution.podCount
		podAllocatableSpaceOnNode := podLimitOnNode - podAllocatedOnNode

		log.Println(fmt.Sprintf("[%s] "+strings.Repeat("+", podAllocatedOnNode)+strings.Repeat("-", podAllocatableSpaceOnNode), node.Name))
	}
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
