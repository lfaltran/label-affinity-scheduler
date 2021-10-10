#!/bin/bash
## Generic installation on all nodes

sysctl -w net.ipv4.ip_forward=1
sysctl -w net.ipv4.conf.all.rp_filter=0
sed -i 's/#net.ipv4.ip_forward=1/net.ipv4.ip_forward=1/g' /etc/sysctl.conf 
sed -i 's/#net.ipv4.conf.default.rp_filter=1/net.ipv4.conf.all.rp_filter=0/g' /etc/sysctl.conf 
sudo sysctl -p /etc/sysctl.conf

# desativando swap
swapoff -a
# problema c/ o comando abaixo na Azure
# sed -i '2s/^/#/' /etc/fstab

apt-get update && apt-get install apt-transport-https ca-certificates curl software-properties-common -y

curl -fsSL https://download.docker.com/linux/ubuntu/gpg | apt-key add -

add-apt-repository \
    "deb [arch=amd64] https://download.docker.com/linux/ubuntu \
    $(lsb_release -cs) \
    stable"
apt-get update && apt-get install -y docker-ce

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

apt-get update && apt-get install -y apt-transport-https curl
curl -s https://packages.cloud.google.com/apt/doc/apt-key.gpg | apt-key add -
cat <<EOF >/etc/apt/sources.list.d/kubernetes.list
deb https://apt.kubernetes.io/ kubernetes-xenial main
EOF

apt-get update
apt install kubernetes-cni -y # not in documentation needed for updates
apt-get install -y kubelet=1.21.5-00 kubeadm=1.21.5-00 kubectl=1.21.5-00
apt-mark hold kubelet kubeadm kubectl
systemctl daemon-reload
systemctl restart kubelet

## Create Default Audit Policy

mkdir -p /etc/kubernetes
cat > /etc/kubernetes/audit-policy.yaml <<EOF
apiVersion: audit.k8s.io/v1beta1
kind: Policy
rules:
- level: Metadata
EOF

# folder to save audit logs
mkdir -p /var/log/kubernetes/audit

## Install NFS Client Drivers
sudo apt-get update
sudo apt-get install -y nfs-common

# Liberação de portas
#https://stackoverflow.com/a/54810101
iptables -P INPUT ACCEPT
iptables -P OUTPUT ACCEPT
iptables -P FORWARD ACCEPT

iptables -I INPUT -s 192.168.0.0/24 -d 192.168.0.0/24 -j ACCEPT
iptables -D FORWARD -j REJECT --reject-with icmp-host-prohibited

# Configurar VPN - https://dev.to/netikras/kubernetes-on-vpn-wireguard-152l
sudo apt-get install -y wireguard-tools net-tools

### AO TÉRMINO ###
#Executar no NODE MASTER -> kubeadm token create --print-join-command
#Executar no NODE WORKER -> kubeadm join k8s-ppgcomp.unioeste.br:6443 ... --node-name

#remover arquivos do CALICO (se houverem)
# /etc/cni/net.d

#https://www.thenoccave.com/2020/08/kubernetes-flannel-failed-to-list-v1-service/
# criar arquivo /run/flannel/subnet.env
# FLANNEL_NETWORK=10.244.0.0/16
# FLANNEL_SUBNET=192.168.0.1/24
# FLANNEL_MTU=8950
# FLANNEL_IPMASQ=true

#https://dev.to/netikras/kubernetes-on-vpn-wireguard-152l

# Gerar arquivo PEER ref. ao IP desejado, obtido da pasta "~/wg/<ip_addres>/peers"
# vi /etc/wireguard/wg0.conf

# subir a interface de rede
# systemctl enable wg-quick@wg0
# systemctl start wg-quick@wg0

# para acompanhar status da interface, digite: wg

# Ajustar informações ref. ao NODE
# vi /etc/default/kubelet
# KUBELET_EXTRA_ARGS=--node-ip={IP_VPN/192.168.0.x}
# systemctl restart kubelet