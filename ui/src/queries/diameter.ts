import { apiFetch } from "@/queries/utils";

export type DiameterPeerState =
  "down" | "connecting" | "open" | "suspect" | "reopen" | "closing";

export interface DiameterPeer {
  id?: number;
  configured: boolean;
  host: string;
  realm: string;
  address: string;
  state: DiameterPeerState;
  applications: string[];
  since: string;
  error?: string;
}

export interface DiameterRoute {
  realm: string;
  default: boolean;
  application: string;
  peers: string[];
}

export interface DiameterStatus {
  host: string;
  realm: string;
  peers: DiameterPeer[];
  routes: DiameterRoute[];
}

export const hssPeers = (status: DiameterStatus): string[] =>
  status.routes.find(
    (route) =>
      route.application === "s6c" &&
      route.realm.toLowerCase() === status.realm.toLowerCase(),
  )?.peers ?? [];

export const getDiameterStatus = (): Promise<DiameterStatus> =>
  apiFetch<DiameterStatus>("/api/v1/diameter");

export type PeerApplication = "s6c" | "sgd";

export interface PeerRoute {
  realm?: string;
  default?: boolean;
  application: PeerApplication;
  priority: number;
}

export interface PeerStatus {
  state: DiameterPeerState;
  host?: string;
  realm?: string;
  since: string;
  error?: string;
}

export interface Peer {
  id: number;
  host: string;
  addresses: string[];
  port: number;
  routes: PeerRoute[];
  status?: PeerStatus;
}

export type PeerParams = Omit<Peer, "id" | "status">;

export const DEFAULT_DIAMETER_PORT = 3868;

const peersPath = "/api/v1/diameter/peers";

export const listPeers = (): Promise<Peer[]> =>
  apiFetch<{ items: Peer[] }>(peersPath).then((r) => r.items);

export const createPeer = (peer: PeerParams): Promise<Peer> =>
  apiFetch<Peer>(peersPath, { method: "POST", body: peer });

export const updatePeer = (id: number, peer: PeerParams): Promise<Peer> =>
  apiFetch<Peer>(`${peersPath}/${id}`, { method: "PUT", body: peer });

export const deletePeer = (id: number): Promise<void> =>
  apiFetch<void>(`${peersPath}/${id}`, { method: "DELETE" });
