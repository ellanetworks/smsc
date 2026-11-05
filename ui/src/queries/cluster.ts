import { apiFetch, withQuery } from "@/queries/utils";

export type DrainState = "active" | "draining" | "drained";

export type Suffrage = "voter" | "nonvoter" | "none";

export interface ClusterMember {
  node_id: string;
  address: string;
  diameter_label: string;
  version: string;
  joined_at: string;
  drain_state: DrainState;
  suffrage: Suffrage;
  leader: boolean;
  healthy?: boolean;
  last_contact?: string;
  applied_index?: number;
  diverged?: boolean;
}

export interface ClusterStatus {
  node_id: string;
  cluster_id: string;
  clustered: boolean;
  leader_id?: string;
  term: number;
  applied_index: number;
  commit_index: number;
  healthy?: boolean;
  failure_tolerance?: number;
  members: ClusterMember[] | null;
}

export interface JoinToken {
  token: string;
  expires_at: string;
}

export const DIAMETER_LABEL = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;

const memberPath = (nodeId: string) =>
  `/api/v1/cluster/members/${encodeURIComponent(nodeId)}`;

export const getCluster = (): Promise<ClusterStatus> =>
  apiFetch<ClusterStatus>("/api/v1/cluster");

export const createJoinToken = (ttlSeconds: number): Promise<JoinToken> =>
  apiFetch<JoinToken>("/api/v1/cluster/tokens", {
    method: "POST",
    body: { ttl_seconds: ttlSeconds },
  });

export const joinCluster = (token: string) =>
  apiFetch<{ message: string }>("/api/v1/cluster/join", {
    method: "POST",
    body: { token },
    anonymous: true,
  });

export const updateDiameterLabel = (nodeId: string, diameterLabel: string) =>
  apiFetch<{ message: string }>(memberPath(nodeId), {
    method: "PUT",
    body: { diameter_label: diameterLabel },
  });

export const drainMember = (nodeId: string) =>
  apiFetch<{ message: string }>(`${memberPath(nodeId)}/drain`, {
    method: "POST",
  });

export const resumeMember = (nodeId: string) =>
  apiFetch<{ message: string }>(`${memberPath(nodeId)}/resume`, {
    method: "POST",
  });

export const removeMember = (nodeId: string, force: boolean) =>
  apiFetch<{ message: string }>(
    withQuery(memberPath(nodeId), { force: force ? "true" : undefined }),
    { method: "DELETE" },
  );

export const shortNodeId = (nodeId: string) => nodeId.slice(0, 8);

export const memberLabel = (member: ClusterMember) =>
  member.diameter_label || shortNodeId(member.node_id);
