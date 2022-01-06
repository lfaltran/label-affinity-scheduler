# Change Log

## [1.11] - 2021-12-12

Atualização de versão do Go Client: 1.16 > 1.17.5
Atualização de versão do Alpine: 3.13 > 3.15
Melhoria na geração de eventos ref. a vinculo entre POD e NODE
Melhoria de desempenho na atribuição dos nós computacionais
Melhoria significativa da performance de escalonamento de PODs
Controle para evitar disparo de eventos repetidos

### Added
- Adicionada verificação de eventos vinculados ao DEPLOYMENT para evitar disparo repetitivo
- Revisão do arquivo RBAC p/ adicionar a permissão do monitoramento de EVENTS do Kubernetes
- Melhoria durante depuração de desempenho durante a listagem dos PODs vinculados aos NODEs
- Controle de eventos jah disparados para evitar FLOOD no ETCD

### Changed
- Ajuste para gerar os eventos de vinculo entre POD e NODE apenas se a operação foi bem sucedida
- Novo parâmetro p/ permitir que durante a execução do custom scheduler seja ignorado o calculo de métricas dos nós computacionais
- Melhoria no processo de coleta de PODs de um NODE, melhoria significativa de performance

### Fixed
- Correção no disparo de eventos ref. a PODs que não estejam em situação RUNNING porém já foram alocados à um Nó Computacional
- Ajuste nos valores de label utilizando operações numéricas, pois o caractere "." eh separador de decimal e não se aplica
- Correção durante utilização de operadores GT, GE, LT e LE em cenários de valores "0"
- Tratativa de erro em cenários onde o NODE possui um valor de afinidade negativo, indicando que estourou algum limite (POD COUNT / MEMORY / CPU / AFINIDADE)

## [1.10] - 2021-12-05

Melhoria no processo de geração dos eventos ref. a "scale up" e "scale down"

### Added
- Adicionando novo evento no objeto "Deployment", para faciliar o acompanhamento e visualização do desempenho geral da distribuição de PODs

### Changed
- Ajuste nas chamadas de eventos referente ao "scale up" e "scale down", sendo agora realizado durante no "Event Handler" de Pods, no método "UpdateFunc"

### Fixed
- Correção no monitoramento de PODs em que o "scheduler" utilizado é diferente do ao personalizado, para que também gere os eventos de "scale up" e "scale down"

## [1.9] - 2021-11-02

Ajustes na exibição de log durante processo de "scale down" dos deployments

### Added

### Changed

### Fixed
- Melhoria no processo de exclusão de PODs (scale down), para que notifique o debug do deployment a cada objeto removido
## [1.8] - 2021-10-23

Nova funcionalidade para flexibilitar a estratégia para coleta de consumo computacional de cada nó participante do cluster

### Added
- Início do desenvolvimento da integração com a API de Métricas “Kube State Metrics”
- Nova estratégia para definição do consumo computacional de cada nó participante do cluster, extraindo dados dos limites definidos na tag “resources”
- Aplicado RPAD no nome dos nós computacionais para melhor organização dos logs

### Changed

### Fixed
- Ajuste na exibição de logs ref. ao resultado na distribuição dos PODs, para nao considerar o nós computacionais c/ "TAINTS" (indisponíveis no cluster)

## [1.7] - 2021-07-11

Melhorias na geração de log geral do custom scheduler, para uma melhor análise comportamental

### Added
- Implementação de log final após conclusão das operações de "scale" de um deployment para exibir a distribuição dos Pods através dos Nodes
- Nova configuração para permitir que o log de distribuição dos Pods seja ativado/desativado para deployments específicos

### Changed
- Melhoria na execução sem os logs ativos, exibindo apenas as informações fundamentais

### Fixed
- Correção da versão utilizada do Metrics Server, aplicando um downgrade da versão v0.5.0 p/ v0.4.1 ajuste necessário para evitar erro de "context deadline exceeded"
- Ajuste de parâmetro (imagePullPolicy) para evitar o download a cada nova adição de um Pod e assim receber erro do Docker HUB "429 - You have reached your pull rate limit"

## [1.6] - 2021-06-18

Ajuste na validação de labels entre POD e NODE

### Added
- Implementação de variável para controlar o “peso” da afinidade de um label

### Changed

### Fixed
- Correção de bug durante verificação dos labels entre POD e NODE, em cenários em que não há definição no NODE e se trata de um label obrigatório

## [1.5] - 2021-06-13

Melhoria no calculo de nós computacionais para receber os PODs, respeitando a quantidade de PODs já alocados

### Added

- Implementação de funcionalidade para validar se há algum Taint no nó computacional, para não considerá-lo durante a tarefa de escalonamento

### Changed

- Melhoria no cálculo de prioridade dos nós computacionais, para considerar a capacidade de PODs e a quantidade já alocada (valor base de 150 pods https://kubernetes.io/docs/setup/best-practices/cluster-large)
- Melhoria no cálculo da prioridade de nós, para não abortar a execução se o pacote Metrics não estiver disponível
### Fixed

- Ajuste no console de erro para melhor identificação referente a exceptions geradas pelo pacote Metrics

## [1.4] - 2021-06-05
  
Versão funcional
 
### Added

- Implementação de wildcard para especificar um label como opcional, junto ao seu operador de comparação. Através do caractere “_”, será interpretado como uma comparação desejável, porém se não for satisfeita, o nó computacional não será descartado, apenas não irá pontuar, ex.: “eq_”, “ne_”, etc.
- Implementação para realizar cálculos envolvendo operadores matemáticos, em que o valor de afinidade será resultado da “distância” entre o esperado pelo POD e o encontrado no NODE
- Implementação do cálculo de recursos computacionais livres para cada nó computacional, utilizado como critério da priorização de qual nó irá receber a carga de trabalho
### Changed
  
- Melhoria na validação de operadores “like” e “notlike”, realizando a tradução dos wildcards “_” e “-” para “?” e “*” respectivamente. Necessário pois os valores aceitos pela engine de labels do kubernetes não compreende caracteres especiais
 
### Fixed

- Ajuste na pontuação dos nós computacionais de acordo com o match de labels
- Ajuste no tipo da variável responsável por armazenar o valor calculado da afinidade de INT para FLOAT, para melhor controle de valores parciais, geralmente utilizados em operações como “ge”, “gt”, “le” e “lt”

## [1.3] - 2021-06-03
 
### Added

- Implementação do envio de variáveis via “args”, durante criação do “scheduler”
- Definição de variável para habilitar/desabilitar mensagens de debug enviadas ao console
- Implementação de novo método para realizar o cálculo de afinidade entre labels dos PODS e NODES
- Definição de lista de operações que serão aceita durante verificação de valores de labels
  - "eq / ne / gt / ge / lt / le / like / notlike / contains / notcontains / in / notin"
- Implementação de regra para validar as operações de comparação entre os labels de PODS e NODE
- Implementação de EventHandler para monitorar a exclusão de PODS

### Changed

- Simplificação ainda maior do arquivo Makefile, responsável pela construção da aplicação em Golang
- Ajuste do log timestamp durante execução em modo “debug”

### Fixed

- Ajuste em métodos para operar com mapas ao invés de listas durante análise dos nós computacionais

## [1.2] - 2021-05-31

Revisão geral de métodos e simplificação de código responsável pela construção do Custom Scheduler

### Added

- Flexibilidade na definição do DNS Prefix a ser utilizado nos labels
- Adicionado monitoramento de recursos ref. DISK EPHEMERAL

### Changed

- Revisão e simplificação do Makefile, responsável pela construção da aplicação em Golang
- Revisão do arquivo “deployments/rbac.yaml” com a inclusão de permissões para acesso aos deployments

### Fixed

- Ajuste de imports para melhor legibilidade entre os pacotes “k8s.io/api/apps/v1” e “k8s.io/api/core/v1”

## [1.1] - 2021-05-30
 
### Added
   
- Adicionado dados referente ao capacidade de disco dos nós computacionais

### Changed

- Melhoria no PRINT realizado no console referente aos labels de NODES e PODS, para considerar o prefixo definido na constante “dnsForLabelPrefix”
 
### Fixed

- Correção do vínculo inicial de um NODE ao POD recebido

## [1.0] - 2021-05-29
 
Não funcional, pois não vincula nenhum NODE ao POD recebido

### Added

- Geração de build inicial do scheduler, contendo implementação em GOLANG.

### Changed
 
### Fixed