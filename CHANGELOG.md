# Change Log

## [1.4] - 2021-06-05
  
Versão funcional
 
### Added

- Implementação de wildcard para especificar um label como opcional, junto ao seu operador de comparação. Através do caractere “_”, será interpretado como uma comparação desejável, porém se não for satisfeita, o nó computacional não será descartado, apenas não irá pontuar, ex.: “eq_”, “ne_”, etc.

### Changed
  
- Melhoria na validação de operadores “like” e “notlike”, realizando a tradução dos wildcards “_” e “-” para “?” e “*” respectivamente. Necessário pois os valores aceitos pela engine de labels do kubernetes não compreende caracteres especiais
 
### Fixed

- Ajuste na pontuação dos nós computacionais de acordo com o match de labels
- Ajuste no tipo da variável responsável por armazenar o valor calculado da afinidade de INT para FLOAT, para melhor controle de valores parciais, geralmente utilizados em

## [1.3] - 2021-06-03
 
### Added

- Implementação do envio de variáveis via “args”, durante criação do “scheduler”
- Definição de variável para habilitar/desabilitar mensagens de debug enviadas ao console
- Implementação de novo método para realizar o cálculo de afinidade entre labels dos PODS e NODES
- Definição de lista de operações que serão aceita durante verificação de valores de labels
-- "eq / ne / gt / ge / lt / le / like / notlike / contains / notcontains / in / notin"
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