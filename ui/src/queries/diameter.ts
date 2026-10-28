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
