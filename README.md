# Abstract

Container allocation policies present in modern orchestration tools, such as Kubernetes, are completely agnostic with respect to specific application requirements or meeting business rules. They usually perform the schedule of applications simply by spreading them among the worker nodes using algorithms such as Round-Robin or First-Fit. Furthermore, when outlining the state of the art, it appears that the proposed strategies do not satisfy the criteria for scheduling applications in real production environments.

This work presents a technique that allows the customization of scheduling as an alternative to the default behavior offered by the orchestration tools of containerized workloads in multi-cloud environments, carrying out pertinent negotiations and validations to achieve the objective of performing the scaling of the application instances to compute nodes with higher affinity. For this, desirable or impositive features are considered, obtained from the requirements phase during the design of the application, or even at the phase of contracting the cloud hosting service.

Looking to offer an alternative to this behavior and in an easy-to-use approach, we propose a custom scheduler that performs an affinity analysis from labels defined in metadata of objects that represent each of the compute nodes and workloads in an orchestrated environment, and as a second feature, prioritize the choice through those nodes with the highest idle computational resources, ensure a result that respects pre-defined rules and restrictions, according to the application business requirements.

<p align="right">(<a href="#top">back to top</a>)</p>

[![Relacionamento de POD c/ Nó Computacional][kube-relacionamento-label-node-workload-v2]]

# How to use

## Label Nodes
```kubectl label nodes <no-computacional> ppgcomp.unioeste.br…```

## Label Deployments
```kubectl patch deployment <carga-trabalho> --type='json'…```

## Deploy Custom Scheduler
```apiVersion: apps/v1
kind: Deployment
metadata:
  name: label-affinity-scheduler
spec:
  template:
    spec:
      containers:
        - name: label-affinity-scheduler
          image: lfaltran/label-affinity-scheduler
```

## Assing Custom Scheduler on Deployment
```apiVersion: apps/v1
kind: Deployment
metadata:
  name: app-a
spec:
  selector:
    matchLabels:
      app: app-a
  template:
    spec:
      schedulerName: label-affinity-scheduler
      containers:
      - name: app-a
        image: hendrikmaus/kubernetes-dummy-image:latest
        imagePullPolicy: IfNotPresent
```

<p align="right">(<a href="#top">back to top</a>)</p>

# Acknowledgement

This work was supported in part by Oracle Cloud credits and related resources provided by the Oracle for Research program.

[kube-relacionamento-label-node-workload-v2]: images/kube-relacionamento-label-node-workload-v2.png