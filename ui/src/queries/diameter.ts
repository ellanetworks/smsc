import { apiFetch } from "@/queries/utils";

export type DiameterPeerState =
  "down" | "connecting" | "open" | "suspect" | "reopen" | "closing";

export interface DiameterPeer {
  // The ID of a configured peer, absent for a peer that connected to the SMSC.
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
  // Empty for a default route.
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

export const getDiameterStatus = (): Promise<DiameterStatus> =>
  apiFetch<DiameterStatus>("/api/v1/diameter");

export type PeerApplication = "s6c" | "sgd";

// PeerRoute sends a peer the requests of an application for a realm, or, for
// a default route, for every other realm but this SMSC's own.
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

// Peer is a Diameter agent that this SMSC connects to, such as a DEA or a DRA.
export interface Peer {
  id: number;
  host: string;
  addresses: string[];
  port: number;
  routes: PeerRoute[];
  // status is in reads only: the SMSC applies a change after it answers.
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
