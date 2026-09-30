import { apiFetch } from "@/queries/utils";

export type DiameterPeerState =
  "down" | "connecting" | "open" | "suspect" | "reopen" | "closing";

export interface DiameterPeer {
  host: string;
  realm: string;
  address: string;
  state: DiameterPeerState;
  applications: string[];
  since: string;
}

export interface DiameterStatus {
  host: string;
  realm: string;
  hss_available: boolean;
  peers: DiameterPeer[];
}

export const getDiameterStatus = (): Promise<DiameterStatus> =>
  apiFetch<DiameterStatus>("/api/v1/diameter");
