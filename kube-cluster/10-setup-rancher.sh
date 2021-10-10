#source
#https://rancher.com/docs/rancher/v2.6/en/installation/install-rancher-on-k8s/

#install helm
snap install helm --classic

#1. Add the Helm Chart Repositorylink
helm repo add rancher-latest https://releases.rancher.com/server-charts/latest

#2. Create a Namespace for Rancher
kubectl create namespace cattle-system

#4. Install cert-managerlink
# If you have installed the CRDs manually instead of with the `--set installCRDs=true` option added to your Helm install command, you should upgrade your CRD resources before upgrading the Helm chart:
kubectl apply -f https://github.com/jetstack/cert-manager/releases/download/v1.5.1/cert-manager.crds.yaml

# Add the Jetstack Helm repository
helm repo add jetstack https://charts.jetstack.io

# Update your local Helm chart repository cache
helm repo update

# Install the cert-manager Helm chart
# helm del cert-manager --namespace cert-manager
# kubectl delete namespace cert-manager
# kubectl delete -f https://github.com/jetstack/cert-manager/releases/download/v1.5.1/cert-manager.crds.yaml
helm install cert-manager jetstack/cert-manager \
  --namespace cert-manager \
  --create-namespace \
  --version v1.5.1 \
  --debug

#5. Install Rancher with Helm and Your Chosen Certificate Optionlink
# helm del rancher --namespace cattle-system
# kubectl delete namespace cattle-system
helm install rancher rancher-latest/rancher \
  --namespace cattle-system \
  --set hostname=k8s-ppgcomp.unioeste.br \
  --set bootstrapPassword=admin \
  --set ingress.tls.source=letsEncrypt \
  --set letsEncrypt.email=luiz.santos2@unioeste.br \
  --set replicas=1

#check status
kubectl -n cattle-system rollout status deploy/rancher

#acesso
#https://k8s-ppgcomp.unioeste.br

helm install rancher rancher-latest/rancher \
  --namespace cattle-system \
  --set hostname=k8s-ppgcomp.unioeste.br