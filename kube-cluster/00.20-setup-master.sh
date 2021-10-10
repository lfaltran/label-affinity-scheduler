# elevando acesso
sudo su

# Step 01: Configure IP Tables
sysctl -w net.ipv4.ip_forward=1
sysctl -w net.ipv4.conf.all.rp_filter=0
sed -i 's/#net.ipv4.ip_forward=1/net.ipv4.ip_forward=1/g' /etc/sysctl.conf 
sed -i 's/#net.ipv4.conf.default.rp_filter=1/net.ipv4.conf.default.rp_filter=0/g' /etc/sysctl.conf 
sudo sysctl -p /etc/sysctl.conf

# Step 02: SWAP OFF
swapoff -a

sed -i '2s/^/#/' /etc/fstab

# Step 03: Install Docker
apt-get update

apt-get update && apt-get install apt-transport-https \
ca-certificates curl software-properties-common -y

curl -fsSL https://download.docker.com/linux/ubuntu/gpg | apt-key add -

add-apt-repository \
"deb [arch=amd64] https://download.docker.com/linux/ubuntu \
$(lsb_release -cs) \
stable"

apt-get update && apt-get install -y docker-ce

# Setup Storage Driver
cat > /etc/docker/daemon.json <<EOF
{
   "exec-opts": ["native.cgroupdriver=systemd"],
   "log-driver": "json-file",
   "log-opts": {
        "max-size": "100m"
   },
   "storage-driver": "overlay2"
}
EOF

mkdir -p /etc/systemd/system/docker.service.d
systemctl daemon-reload
systemctl restart docker
systemctl enable docker

# Step 04: kubeadm, kubelet and kubectl
curl -s https://packages.cloud.google.com/apt/doc/apt-key.gpg | apt-key add -

cat <<EOF >/etc/apt/sources.list.d/kubernetes.list
deb https://apt.kubernetes.io/ kubernetes-xenial main
EOF

apt-get update
apt install kubernetes-cni -y
apt-get install -y kubelet=1.21.5-00 kubeadm=1.21.5-00 kubectl=1.21.5-00
apt-mark hold kubelet kubeadm kubectl
systemctl daemon-reload
systemctl restart kubelet

swapoff -a
systemctl start kubelet 

# Set cluster endpoint DNS name

vi /etc/hosts
{private_ip} k8s-ppgcomp.unioeste.br

# review ports and protocols
#https://kubernetes.io/docs/reference/ports-and-protocols/

#ppgcomp-cluster.yaml

#v1 - flannel
# apiVersion: kubeadm.k8s.io/v1beta2
# kind: ClusterConfiguration
# networking:
#   podSubnet: 10.244.0.0/16
#   serviceSubnet: 10.96.0.0/12
# controlPlaneEndpoint: k8s-ppgcomp.unioeste.br
# clusterName: ppgcomp
# scheduler: {}

#v2 - calico
apiVersion: kubeadm.k8s.io/v1beta2
kind: InitConfiguration
nodeRegistration:
   name: oracle-master-01
localAPIEndpoint:
  advertiseAddress: 192.168.0.1
---
apiVersion: kubeadm.k8s.io/v1beta2
kind: ClusterConfiguration
networking:
  podSubnet: 10.240.0.0/16
  serviceSubnet: 10.96.0.0/12
controlPlaneEndpoint: k8s-ppgcomp.unioeste.br
clusterName: ppgcomp

#v3 - calico
# apiVersion: kubeadm.k8s.io/v1beta2
# kind: InitConfiguration
# nodeRegistration:
#    name: "k8smaster"
# localAPIEndpoint:
#   advertiseAddress: "10.100.0.1"
# ---
# apiVersion: kubeadm.k8s.io/v1beta2
# kind: ClusterConfiguration
# etcd:
#    local:
#       serverCertSANs:
#          - "k8s-ppgcomp.unioeste.br"
#       peerCertSANs:
#          - "10.100.0.1"
#       extraArgs:
#          # listen-peer-urls: "https://10.100.0.1:2380"
#          listen-client-urls: "https://10.100.0.1:2379"
#          # advertise-client-urls: "https://10.100.0.1:2379"
#          # initial-advertise-peer-urls: "https://10.100.0.1:2380"
# networking:
#   serviceSubnet: "172.16.10.0/12"
#   podSubnet: "10.100.0.1/24"
# controlPlaneEndpoint: "10.100.0.1:6443"
# apiServer:
#    certSANs:
#       - "10.100.1.1"
#       - "k8s-ppgcomp.unioeste.br"
# scheduler:
#    extraArgs:
#       address: "10.100.0.1"
# clusterName: "ppgcomp"

# via command line
kubeadm init \
  --pod-network-cidr=192.168.0.0/16 \
  --control-plane-endpoint=k8s-ppgcomp.unioeste.br

# via arquivo de configuração
kubeadm init --config=ppgcomp-cluster-v2.yaml

#setup kubectl
mkdir -p $HOME/.kube
mkdir -p /home/ubuntu/.kube

cp -rf /etc/kubernetes/admin.conf $HOME/.kube/config
cp -rf /etc/kubernetes/admin.conf /home/ubuntu/.kube/config

chown $(id -u):$(id -g) $HOME/.kube/config
chown ubuntu:ubuntu /home/ubuntu/.kube/config

# copiando p/ localhost
# scp -C -i ssh-key-oracle-cloud-lfaltran.key ubuntu@129.146.171.94:/home/ubuntu/.kube/config /home/lfaltran/.kube

#check cluster status
kubectl cluster-info

#Enable PODs on Master Node
kubectl taint nodes --all node-role.kubernetes.io/master-

# Install Network Add-On => Calico
# kubectl apply -f calico.yaml
# kubectl set env daemonset/calico-node -n kube-system IP_AUTODETECTION_METHOD=can-reach=www.google.com
# kubectl apply -f https://docs.projectcalico.org/manifests/calico.yaml


# Install Network Add-On => Flannel
# kubectl apply -f https://raw.githubusercontent.com/coreos/flannel/master/Documentation/kube-flannel.yml

# Install Network Add-On => Weave
# https://medium.com/codeops/installing-weave-cni-on-aws-eks-51c2e6b7abc8

kubectl get pods -n kube-system

#Join the worker nodes to the Cluster
# ***** KUBEADM JOIN *****
kubeadm token create --print-join-command

kubeadm join k8s-ppgcomp.unioeste.br:6443 --token l7zin3.y4usoxbvmc7qrcxl \
	--discovery-token-ca-cert-hash sha256:85ee449bcf60d38b140952f936f52427db120030bfa3b7023e21e7799819f336

# Liberação de portas
#https://stackoverflow.com/a/54810101
iptables -P INPUT ACCEPT
iptables -P OUTPUT ACCEPT
iptables -P FORWARD ACCEPT

iptables -I INPUT -s 192.168.0.0/24 -d 192.168.0.0/24 -j ACCEPT
iptables -D FORWARD -j REJECT --reject-with icmp-host-prohibited

# setup da VPN - Wireguard
#https://dev.to/netikras/kubernetes-on-vpn-wireguard-152l
# gerar arquivo 
# ./wggen.bash 192.168.0
# copiar ./wg/192.168.0.0/wg0.conf p/ /etc/wireguard/wg0.conf
systemd enable wg-quick@wg0
systemd start wg-quick@wg0