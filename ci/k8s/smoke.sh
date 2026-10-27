#!/usr/bin/env bash
# Smoke test of the Kubernetes manifests in k8s/: deploys them, through the
# overlay ci/k8s/<MODE>, on a single-node kind cluster, and checks the SMSC over
# its API Service, over Diameter, across a restart and, when PREVIOUS_IMAGE is
# set, across an upgrade. This host plays the core with the load client of
# internal/loadtest.
#
# MODE multus deploys k8s/. The kind node gets a second interface, eth1, on a
# Docker network that stands for the signalling network: 192.168.252.0/24, as
# in the manifests, so the NAD and the ConfigMap are deployed as shipped. This
# host is 192.168.252.1 on it, and reaches Diameter and the API on the SMSC's
# Multus address.
#
# MODE pod-network deploys k8s/pod-network/, with no Multus. This host reaches
# Diameter and the API on the node, through their NodePort Services.
#
# Needs docker, kind, kubectl, kubeconform, go, curl, jq and the sctp kernel
# module. Environment:
#   MODE            multus or pod-network (multus)
#   IMAGE           the image under test, in the local Docker (ella-smsc:latest)
#   PREVIOUS_IMAGE  an image to deploy first and upgrade from (none)
#   SIGNALLING_NET  the first three octets of the signalling network
#                   (192.168.252), to change on a host that already has it
#   KEEP            set to keep the cluster and the network afterwards
set -euo pipefail

MODE="${MODE:-multus}"
IMAGE="${IMAGE:-ella-smsc:latest}"
PREVIOUS_IMAGE="${PREVIOUS_IMAGE:-}"

CLUSTER=smsc-smoke
NODE="${CLUSTER}-control-plane"
NETWORK=smsc-signalling
NET="${SIGNALLING_NET:-192.168.252}"
SUBNET="${NET}.0/24"
# Docker gives addresses from the upper half only, so the SMSC's address stays free.
IP_RANGE="${NET}.128/25"
GATEWAY="${NET}.1"
# The address in the NAD and the ConfigMap of k8s/.
SHIPPED_IP=192.168.252.7
SMSC_IP="${NET}.7"
NS=ella

MULTUS_VERSION=v4.3.1
CNI_PLUGINS_VERSION=v1.9.1

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$(mktemp -d)"
WATCH_PID=""

k() { kubectl -n "${NS}" "$@"; }

step() { echo "::group::$*"; }
endstep() { echo "::endgroup::"; }

fail() {
  echo "::error::$*"
  exit 1
}

cleanup() {
  status=$?
  [ -n "${WATCH_PID}" ] && kill "${WATCH_PID}" 2>/dev/null || true
  if [ "${status}" -ne 0 ]; then
    echo "== Diagnostics"
    k get all,pvc,network-attachment-definitions -o wide || true
    k describe pod ella-smsc-0 || true
    k logs ella-smsc-0 --tail=100 || true
    k logs ella-smsc-0 --previous --tail=100 2>/dev/null || true
    kubectl -n kube-system logs -l app=multus --tail=50 || true
  fi
  if [ -z "${KEEP:-}" ]; then
    kind delete cluster --name "${CLUSTER}" >/dev/null 2>&1 || true
    docker network rm "${NETWORK}" >/dev/null 2>&1 || true
  fi
  rm -rf "${WORK}"
  exit "${status}"
}
trap cleanup EXIT

case "${MODE}" in
multus | pod-network) ;;
*) fail "MODE must be multus or pod-network, not ${MODE}" ;;
esac

# Where this host reaches the SMSC, set once it is deployed.
SMSC_ADDR=""
DIAMETER_PORT=3868
API_PORT=5010

# The admin that the smoke test creates, and the API token with which it calls
# the API, set by log_in.
ADMIN_EMAIL=admin@example.com
ADMIN_PASSWORD="smoke test password"
TOKEN=""

# The JSON result of an API call, with the API token once there is one.
api() {
  local auth=()
  [ -z "${TOKEN}" ] || auth=(-H "Authorization: Bearer ${TOKEN}")
  curl -fsS --max-time 10 "${auth[@]}" "http://${SMSC_ADDR}:${API_PORT}/api/v1$1" "${@:2}"
}

# Creates the admin if the SMSC has no user yet, logs it in, and creates the
# API token that api sends from then on. Unlike an access token, it outlives
# the smoke test.
log_in() {
  local credentials jwt
  credentials="$(jq -n --arg email "${ADMIN_EMAIL}" --arg password "${ADMIN_PASSWORD}" '{email: $email, password: $password}')"
  TOKEN=""
  api /init -X POST -H 'Content-Type: application/json' -d "${credentials}" >/dev/null 2>&1 || true
  jwt="$(api /auth/login -X POST -H 'Content-Type: application/json' -d "${credentials}" | jq -er .result.token)" \
    || fail "cannot log in as ${ADMIN_EMAIL}"
  TOKEN="${jwt}"
  TOKEN="$(api /users/me/api-tokens -X POST -H 'Content-Type: application/json' -d '{"name": "smoke test"}' \
    | jq -er .result.token)" || fail "cannot create an API token"
}

node_port() { k get service "$1" -o jsonpath="{.spec.ports[?(@.name==\"$2\")].nodePort}"; }

# Waits for ella-smsc-0 to be ready, not being deleted, and another pod than
# the one of UID $1, if given: the replacement of a deleted pod has its name.
wait_ready() {
  k rollout status statefulset/ella-smsc --timeout=180s
  local deadline=$((SECONDS + 120)) uid deleting ready
  while [ "${SECONDS}" -lt "${deadline}" ]; do
    IFS='|' read -r uid deleting ready < <(k get pod ella-smsc-0 \
      -o jsonpath='{.metadata.uid}|{.metadata.deletionTimestamp}|{.status.conditions[?(@.type=="Ready")].status}{"\n"}' 2>/dev/null || echo '||')
    if [ -n "${uid}" ] && [ "${uid}" != "${1:-}" ] && [ -z "${deleting}" ] && [ "${ready}" = True ]; then
      return 0
    fi
    sleep 1
  done
  fail "ella-smsc-0 did not become ready"
}

# Waits for the API to answer, through the address and port this host uses.
wait_api() {
  for _ in $(seq 30); do
    api /status >/dev/null 2>&1 && return 0
    sleep 1
  done
  fail "the API does not answer at ${SMSC_ADDR}:${API_PORT}"
}

# Deploys the overlay with the given image, loaded into the cluster as ella-smsc:ci.
deploy() {
  docker tag "$1" ella-smsc:ci
  kind load docker-image ella-smsc:ci --name "${CLUSTER}"
  kubectl kustomize "${ROOT}/ci/k8s/${MODE}" | sed "s/${SHIPPED_IP//./\\.}/${SMSC_IP}/g" | kubectl apply -f -
  # The tag does not change between deploys, so restart to run the new image.
  local old=""
  if [ "${2:-}" = restart ]; then
    old="$(k get pod ella-smsc-0 -o jsonpath='{.metadata.uid}')"
    k rollout restart statefulset/ella-smsc
  fi
  wait_ready "${old}"

  if [ "${MODE}" = multus ]; then
    SMSC_ADDR="${SMSC_IP}"
  else
    SMSC_ADDR="$(kubectl get node "${NODE}" -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}')"
    DIAMETER_PORT="$(node_port ella-smsc-diameter diameter)"
    API_PORT="$(node_port ella-smsc http)"
  fi
  wait_api
}

step "Lint the manifests"
version="$(cat "${ROOT}/version/VERSION")"
for dir in k8s k8s/pod-network ci/k8s/multus ci/k8s/pod-network; do
  kubectl kustomize "${ROOT}/${dir}" | kubeconform -strict -summary -skip NetworkAttachmentDefinition
done
endstep

step "Create the cluster"
if [ "${MODE}" = multus ]; then
  if ip -4 route show to match "${GATEWAY}" | grep -qv '^default'; then
    fail "${SUBNET} is already routed on this host; set SIGNALLING_NET to a free /24"
  fi
  docker network create "${NETWORK}" --subnet "${SUBNET}" --ip-range "${IP_RANGE}" --gateway "${GATEWAY}" >/dev/null
fi
kind create cluster --name "${CLUSTER}" --wait 120s
if [ "${MODE}" = multus ]; then
  docker network connect "${NETWORK}" "${NODE}"
  docker exec "${NODE}" ip -br addr show eth1
  LOCAL_IP="${GATEWAY}"
else
  # This host on the kind network.
  LOCAL_IP="$(docker network inspect kind -f '{{range .IPAM.Config}}{{.Gateway}} {{end}}' | tr ' ' '\n' | grep -m1 -F .)"
fi
endstep

if [ "${MODE}" = multus ]; then
step "Install the CNI plugins and Multus"
curl -fsSL "https://github.com/containernetworking/plugins/releases/download/${CNI_PLUGINS_VERSION}/cni-plugins-linux-amd64-${CNI_PLUGINS_VERSION}.tgz" \
  | tar -xz -C "${WORK}" ./macvlan ./static
docker cp "${WORK}/macvlan" "${NODE}:/opt/cni/bin/macvlan"
docker cp "${WORK}/static" "${NODE}:/opt/cni/bin/static"
curl -fsSL "https://raw.githubusercontent.com/k8snetworkplumbingwg/multus-cni/${MULTUS_VERSION}/deployments/multus-daemonset-thick.yml" \
  | sed "s|multus-cni:snapshot-thick|multus-cni:${MULTUS_VERSION}-thick|" \
  | kubectl apply -f -
kubectl -n kube-system rollout status daemonset/kube-multus-ds --timeout=180s
endstep
fi

if [ -n "${PREVIOUS_IMAGE}" ]; then
  step "Deploy the previous release, ${PREVIOUS_IMAGE}"
  docker image inspect "${PREVIOUS_IMAGE}" >/dev/null 2>&1 || docker pull "${PREVIOUS_IMAGE}"
  deploy "${PREVIOUS_IMAGE}"
  api /status
  echo
  api /messages -X POST -H 'Content-Type: application/json' \
    -d '{"from": "+15550001000", "to": "+15551230002", "text": "stored before the upgrade"}' >/dev/null
  endstep

  step "Upgrade to ${IMAGE}"
  deploy "${IMAGE}" restart
  log_in
  api /messages | jq -e '.result.items | any(.text == "stored before the upgrade")' >/dev/null \
    || fail "the message stored before the upgrade is gone"
  endstep
else
  step "Deploy ${IMAGE}"
  deploy "${IMAGE}"
  log_in
  endstep
fi

step "Check the pod"
kubectl get namespace "${NS}" -o jsonpath='{.metadata.labels.pod-security\.kubernetes\.io/enforce}' | grep -qx restricted \
  || fail "the namespace does not enforce the restricted Pod Security Standard"
sleep 30
restarts="$(k get pod ella-smsc-0 -o jsonpath='{.status.containerStatuses[0].restartCount}')"
[ "${restarts}" = 0 ] || fail "the SMSC restarted ${restarts} times"
k get pod ella-smsc-0 -o wide
endstep

step "Check the API through the Service"
node_ip="$(kubectl get node "${NODE}" -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}')"
api_port="$(node_port ella-smsc http)"
running="$(curl -fsS --max-time 10 "http://${node_ip}:${api_port}/api/v1/status" | jq -r .result.version)"
[ "${running}" = "${version}" ] || fail "the SMSC reports version ${running}, want ${version}"
echo "version ${running} at ${node_ip}:${api_port}"
endstep

step "Check the Diameter address"
want="${SMSC_IP}"
if [ "${MODE}" = pod-network ]; then
  want="$(k get pod ella-smsc-0 -o jsonpath='{.status.podIP}')"
fi
k logs ella-smsc-0 | grep '"msg":"smsc started"' | jq -e --arg want "${want}:3868" '.diameter == $want' >/dev/null \
  || fail "the SMSC does not listen for Diameter on ${want}:3868"
echo "Diameter on ${want}:3868"
endstep

step "Exchange short messages over Diameter at ${SMSC_ADDR}:${DIAMETER_PORT}"
(cd "${ROOT}" && go test -c -o "${WORK}/loadtest.test" ./internal/loadtest)
"${WORK}/loadtest.test" -test.run TestLoad -test.v -load -smsc "${SMSC_ADDR}" -local "${LOCAL_IP}" \
  -diameter-port "${DIAMETER_PORT}" -api-port "${API_PORT}" -email "${ADMIN_EMAIL}" -password "${ADMIN_PASSWORD}" \
  -subs 100 -rates 20 -step 10s -drain 30s
# The load client reports, but does not check, the deliveries. The message
# stored before the upgrade is to a phone no core serves, so it stays pending.
for status in pending failed expired; do
  left="$(api "/messages?status=${status}&per_page=100" \
    | jq '[.result.items[] | select(.text != "stored before the upgrade")] | length')"
  [ "${left}" = 0 ] || fail "${left} short messages are ${status} after the load"
done
last="$(api '/messages?status=delivered&per_page=1' | jq -e '.result.items[0].id')" \
  || fail "no short message was delivered"
echo "delivered up to message ${last}"
endstep

step "Restart the pod"
uid="$(k get pod ella-smsc-0 -o jsonpath='{.metadata.uid}')"
# The pod keeps its name, so its logs and exit code are gone once it is
# replaced: follow them from before the deletion.
k logs -f ella-smsc-0 >"${WORK}/stopped.log" 2>&1 &
k get pod ella-smsc-0 -w -o jsonpath='{.metadata.uid} {.status.containerStatuses[0].state.terminated.exitCode}{"\n"}' \
  >"${WORK}/watch.txt" 2>&1 &
WATCH_PID=$!
k delete pod ella-smsc-0 --wait=false
wait_ready "${uid}"
wait_api
kill "${WATCH_PID}"
WATCH_PID=""

grep -q '"msg":"smsc stopping"' "${WORK}/stopped.log" || fail "the SMSC did not log its shutdown"
if grep -q 'deliveries still in flight' "${WORK}/stopped.log"; then
  fail "the SMSC stopped with deliveries in flight"
fi
codes="$(awk -v uid="${uid}" '$1 == uid && NF == 2 { print $2 }' "${WORK}/watch.txt" | sort -u)"
[ "${codes}" = 0 ] || fail "the SMSC exited with code '${codes}', want 0"
tail -n 3 "${WORK}/stopped.log"

api "/messages/${last}" | jq -e '.result.status == "delivered"' >/dev/null \
  || fail "message ${last} is not in the database after the restart"
echo "message ${last} survived the restart"
endstep

echo "Kubernetes smoke test passed"
