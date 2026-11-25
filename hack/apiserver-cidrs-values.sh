#!/usr/bin/env bash

# Copyright 2026 The YipYap Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Prints a Helm values file that sets networkPolicy.apiServerCIDRs and
# networkPolicy.apiServerPorts for the cluster in the current kubectl
# context. It lists each endpoint of the default/kubernetes Service as a
# single-address CIDR, plus the Service ClusterIP for CNIs that evaluate
# policy before the Service is translated. The ports are the endpoint ports
# (for example 6443, or 16443 on MicroK8s) and the Service port.
#
# Managed control planes can move their endpoints during upgrades. There,
# prefer the control plane subnet over the /32 entries this prints.
#
# Usage:
#   hack/apiserver-cidrs-values.sh > apiserver-values.yaml
#   helm install yipyap-source ./chart/yipyap-source -f apiserver-values.yaml ...

set -euo pipefail

endpoints=$(kubectl get endpointslices -n default \
  -l kubernetes.io/service-name=kubernetes \
  -o jsonpath='{range .items[*].endpoints[*]}{range .addresses[*]}{@}{"\n"}{end}{end}')
endpoint_ports=$(kubectl get endpointslices -n default \
  -l kubernetes.io/service-name=kubernetes \
  -o jsonpath='{range .items[*].ports[*]}{.port}{"\n"}{end}')
cluster_ip=$(kubectl get service kubernetes -n default -o jsonpath='{.spec.clusterIP}')
service_ports=$(kubectl get service kubernetes -n default \
  -o jsonpath='{range .spec.ports[*]}{.port}{"\n"}{end}')

addrs=$(printf '%s\n%s\n' "${endpoints}" "${cluster_ip}" | sed '/^$/d' | sort -u)
ports=$(printf '%s\n%s\n' "${endpoint_ports}" "${service_ports}" | sed '/^$/d' | sort -n -u)
if [[ -z "${addrs}" ]]; then
  echo "no kube-apiserver addresses found in default/kubernetes" >&2
  exit 1
fi
if [[ -z "${ports}" ]]; then
  echo "no kube-apiserver ports found in default/kubernetes" >&2
  exit 1
fi

# Check every value before printing anything, so a failure never leaves a
# partial values file behind.
cidrs=()
while read -r addr; do
  if [[ "${addr}" =~ ^[0-9]{1,3}(\.[0-9]{1,3}){3}$ ]]; then
    cidrs+=("${addr}/32")
  elif [[ "${addr}" =~ ^[0-9a-fA-F:.]+$ && "${addr}" == *:* ]]; then
    cidrs+=("${addr}/128")
  else
    echo "unexpected kube-apiserver address: ${addr}" >&2
    exit 1
  fi
done <<< "${addrs}"
port_list=()
while read -r port; do
  if ! [[ "${port}" =~ ^[0-9]+$ ]] || ((port < 1 || port > 65535)); then
    echo "unexpected kube-apiserver port: ${port}" >&2
    exit 1
  fi
  port_list+=("${port}")
done <<< "${ports}"

echo "networkPolicy:"
echo "  apiServerCIDRs:"
for cidr in "${cidrs[@]}"; do
  echo "    - \"${cidr}\""
done
echo "  apiServerPorts:"
for port in "${port_list[@]}"; do
  echo "    - ${port}"
done
