#!/usr/bin/env bash
set -euo pipefail

MODE="${MODE:-multus}"
IMAGE="${IMAGE:-ella-smsc:latest}"
PREVIOUS_IMAGE="${PREVIOUS_IMAGE:-}"

CLUSTER=smsc-smoke
NODE="${CLUSTER}-control-plane"
NETWORK=smsc-signalling
NET="${SIGNALLING_NET:-192.168.252}"
SUBNET="${NET}.0/24"
IP_RANGE="${NET}.128/25"
GATEWAY="${NET}.1"
SHIPPED_IP=192.168.252.7
SMSC_IP="${NET}.7"
NS=ella

MULTUS_VERSION=v4.3.1
CNI_PLUGINS_VERSION=v1.9.1

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$(mktemp -d)"
WATCH_PID=""
FORWARD_PID=""

k() { kubectl -n "${NS}" "$@"; }

step() { echo "::group::$*"; }
endstep() { echo "::endgroup::"; }

fail() {
  echo "::error::$*" >&2
  exit 1
}

cleanup() {
  status=$?
  [ -n "${WATCH_PID}" ] && kill "${WATCH_PID}" 2>/dev/null || true
  [ -n "${FORWARD_PID}" ] && kill "${FORWARD_PID}" 2>/dev/null || true
  if [ "${status}" -ne 0 ]; then
    echo "== Diagnostics"
    k get all,pvc,pdb,networkpolicy,network-attachment-definitions -o wide || true
    for pod in $(k get pods -o name 2>/dev/null); do
      k describe "${pod}" || true
      k logs "${pod}" --tail=100 || true
      k logs "${pod}" --previous --tail=100 2>/dev/null || true
    done
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
multus | pod-network | ha) ;;
*) fail "MODE must be multus, pod-network or ha, not ${MODE}" ;;
esac

SMSC_ADDR=""
DIAMETER_PORT=3868
API_PORT=5010

ADMIN_EMAIL=admin@example.com
ADMIN_PASSWORD="smoke test password"
TOKEN=""

api() {
  local auth=()
  [ -z "${TOKEN}" ] || auth=(-H "Authorization: Bearer ${TOKEN}")
  curl -fsS --max-time 10 "${auth[@]}" "http://${SMSC_ADDR}:${API_PORT}/api/v1$1" "${@:2}"
}

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

wait_api() {
  for _ in $(seq 30); do
    api /status >/dev/null 2>&1 && return 0
    sleep 1
  done
  fail "the API does not answer at ${SMSC_ADDR}:${API_PORT}"
}

deploy() {
  docker tag "$1" ella-smsc:ci
  kind load docker-image ella-smsc:ci --name "${CLUSTER}"
  kubectl kustomize "${ROOT}/ci/k8s/${MODE}" | sed "s/${SHIPPED_IP//./\\.}/${SMSC_IP}/g" | kubectl apply -f -
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

PODS=(ella-smsc-0 ella-smsc-1 ella-smsc-2)
FORWARD_PORT=15010

forward() {
  if [ -n "${FORWARD_PID}" ]; then
    kill "${FORWARD_PID}" 2>/dev/null || true
    wait "${FORWARD_PID}" 2>/dev/null || true
  fi
  kubectl -n "${NS}" port-forward "pod/$1" "${FORWARD_PORT}:5010" >"${WORK}/forward.log" 2>&1 &
  FORWARD_PID=$!
  SMSC_ADDR=127.0.0.1
  API_PORT="${FORWARD_PORT}"
  wait_api
}

wait_all_ready() {
  k rollout status statefulset/ella-smsc --timeout=300s
  k wait --for=condition=Ready pod -l app.kubernetes.io/name=ella-smsc --timeout=180s
}

initialize() {
  local credentials code
  credentials="$(jq -n --arg email "${ADMIN_EMAIL}" --arg password "${ADMIN_PASSWORD}" '{email: $email, password: $password}')"
  for _ in $(seq 60); do
    code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 10 -X POST -H 'Content-Type: application/json' \
      -d "${credentials}" "http://${SMSC_ADDR}:${API_PORT}/api/v1/init" || true)"
    case "${code}" in
    201 | 403) return 0 ;;
    esac
    sleep 5
  done
  fail "cannot create the first admin on ella-smsc-0, last answer ${code}"
}

forward_leader() {
  local leader=""
  forward ella-smsc-0
  for _ in $(seq 30); do
    leader="$(api /cluster | jq -r '.result.members[] | select(.leader) | .diameter_label')"
    [ -n "${leader}" ] && break
    sleep 2
  done
  [ -n "${leader}" ] || fail "the cluster has no leader"
  echo "leader ${leader}"
  forward "${leader}"
}

check_members() {
  local cluster voters=false
  for _ in $(seq 30); do
    cluster="$(api /cluster)"
    if echo "${cluster}" | jq -e '
      [.result.members[] | select(.suffrage == "voter") | .diameter_label] | sort == ["ella-smsc-0", "ella-smsc-1", "ella-smsc-2"]
    ' >/dev/null; then
      voters=true
      break
    fi
    sleep 2
  done
  echo "${cluster}" | jq -c '.result.members[] | {node_id, address, diameter_host, suffrage, healthy}'
  [ "${voters}" = true ] || fail "the cluster does not have the three pods as voters"
  echo "${cluster}" | jq -e '.result.members | all(.address | test("^ella-smsc-[0-2]\\.ella-smsc-internal\\."))' >/dev/null \
    || fail "a member does not advertise its per-pod DNS name"
  echo "${cluster}" | jq -r '[.result.members[].node_id] | sort | join(" ")'
}

ha_smoke() {
  step "Deploy ${IMAGE}"
  docker tag "${IMAGE}" ella-smsc:ci
  kind load docker-image ella-smsc:ci --name "${CLUSTER}"
  kubectl apply -k "${ROOT}/ci/k8s/namespace"
  k create secret generic ella-smsc-cluster --from-literal=secret="$(head -c 32 /dev/urandom | base64)"
  kubectl apply -k "${ROOT}/ci/k8s/ha"
  k get pods -o wide
  endstep

  step "Form the cluster and create the first admin on ella-smsc-0"
  for _ in $(seq 60); do
    [ "$(k get pods -l app.kubernetes.io/name=ella-smsc -o name | wc -l)" = 3 ] && break
    sleep 2
  done
  k wait --for=jsonpath='{.status.phase}'=Running pod -l app.kubernetes.io/name=ella-smsc --timeout=180s
  forward ella-smsc-0
  initialize
  wait_all_ready
  k get pods -o wide
  log_in
  local cluster_id members
  cluster_id="$(api /cluster | jq -er .result.cluster_id)"
  members="$(check_members)"
  echo "${members}"
  for pod in "${PODS[@]:1}"; do
    forward "${pod}"
    [ "$(api /cluster | jq -r .result.cluster_id)" = "${cluster_id}" ] || fail "${pod} is in another cluster"
  done
  endstep

  step "Rolling restart"
  forward ella-smsc-0
  local stored
  stored="$(api /messages -X POST -H 'Content-Type: application/json' \
    -d '{"from": "+15550001000", "to": "+15551230002", "text": "stored before the rolling restart"}' | jq -e '.result.items[0].id')"
  k rollout restart statefulset/ella-smsc
  wait_all_ready
  forward_leader
  for _ in $(seq 30); do
    curl -fsS --max-time 10 "http://${SMSC_ADDR}:${API_PORT}/api/v1/readyz?cluster=true" >/dev/null 2>&1 && break
    sleep 2
  done
  curl -fsS --max-time 10 "http://${SMSC_ADDR}:${API_PORT}/api/v1/readyz?cluster=true" >/dev/null \
    || fail "the cluster is not healthy after the rolling restart"
  local after
  after="$(check_members)"
  echo "${after}"
  [ "$(tail -n 1 <<<"${after}")" = "$(tail -n 1 <<<"${members}")" ] \
    || fail "the members changed across the rolling restart"
  [ "$(api /cluster | jq -r .result.cluster_id)" = "${cluster_id}" ] || fail "the cluster changed across the rolling restart"
  api "/messages/${stored}/content" | jq -e '.result.text == "stored before the rolling restart"' >/dev/null \
    || fail "the message stored before the rolling restart is gone"
  for pod in "${PODS[@]}"; do
    restarts="$(k get pod "${pod}" -o jsonpath='{.status.containerStatuses[0].restartCount}')"
    [ "${restarts}" = 0 ] || fail "${pod} restarted ${restarts} times"
  done
  k get pods -o wide
  endstep
}

step "Lint the manifests"
version="$(cat "${ROOT}/version/VERSION")"
for dir in k8s k8s/ha k8s/examples/multus ci/k8s/multus ci/k8s/pod-network ci/k8s/ha; do
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
if [ "${MODE}" = ha ]; then
  cat >"${WORK}/kind.yaml" <<'KIND'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
- role: worker
- role: worker
- role: worker
KIND
  kind create cluster --name "${CLUSTER}" --config "${WORK}/kind.yaml" --wait 120s
else
  kind create cluster --name "${CLUSTER}" --wait 120s
fi
if [ "${MODE}" = multus ]; then
  docker network connect "${NETWORK}" "${NODE}"
  docker exec "${NODE}" ip -br addr show eth1
  LOCAL_IP="${GATEWAY}"
else
  LOCAL_IP="$(docker network inspect kind -f '{{range .IPAM.Config}}{{.Gateway}} {{end}}' | tr ' ' '\n' | grep -m1 -F .)"
fi
endstep

if [ "${MODE}" = ha ]; then
  ha_smoke
  echo "Kubernetes smoke test passed"
  exit 0
fi

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
  BEFORE_UPGRADE="$(api /messages -X POST -H 'Content-Type: application/json' \
    -d '{"from": "+15550001000", "to": "+15551230002", "text": "stored before the upgrade"}' | jq -e '.result.items[0].id')"
  endstep

  step "Upgrade to ${IMAGE}"
  deploy "${IMAGE}" restart
  log_in
  api "/messages/${BEFORE_UPGRADE}/content" | jq -e '.result.text == "stored before the upgrade"' >/dev/null \
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
for status in pending failed expired; do
  left="$(api "/messages?status=${status}&per_page=100" \
    | jq --argjson before "${BEFORE_UPGRADE:-0}" '[.result.items[] | select(.id != $before)] | length')"
  [ "${left}" = 0 ] || fail "${left} short messages are ${status} after the load"
done
last="$(api '/messages?status=delivered&per_page=1' | jq -e '.result.items[0].id')" \
  || fail "no short message was delivered"
echo "delivered up to message ${last}"
endstep

step "Restart the pod"
uid="$(k get pod ella-smsc-0 -o jsonpath='{.metadata.uid}')"
k logs -f ella-smsc-0 >"${WORK}/stopped.log" 2>&1 &
kubectl -n "${NS}" get pod ella-smsc-0 -w -o jsonpath='{.metadata.uid} {.status.containerStatuses[0].state.terminated.exitCode}{"\n"}' \
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
