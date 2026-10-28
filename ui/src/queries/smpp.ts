import { apiFetch } from "@/queries/utils";

export type BindType = "transmitter" | "receiver" | "transceiver";

// SMPPSession is an SMPP session an ESME has bound.
export interface SMPPSession {
  bind_type: BindType;
  remote_address: string;
  bound_at: string;
}

// ESME is an SMPP client account. The messages it submits are attributed to its user.
export interface ESME {
  system_id: string;
  user: string;
  allowed_networks: string[];
  created_at: string;
  sessions: SMPPSession[];
}

// ESMEUpdate is what may change of an ESME. Without a password, the password is unchanged.
export interface ESMEUpdate {
  password?: string;
  allowed_networks: string[];
}

// ESMECreate is a new ESME. Without a password, the server generates one.
export interface ESMECreate extends ESMEUpdate {
  system_id: string;
  user: string;
}

// CreatedESME is a new ESME, with the password the server generated for it, if it did: it is not given again.
export type CreatedESME = ESME & { password?: string };

// MAX_SESSIONS is the most sessions an ESME binds at once.
export const MAX_SESSIONS = 4;
export const MAX_SYSTEM_ID_LENGTH = 15;
export const MAX_PASSWORD_LENGTH = 8;

const esmesPath = "/api/v1/esmes";

const esmePath = (systemID: string) =>
  `${esmesPath}/${encodeURIComponent(systemID)}`;

export const listESMEs = (): Promise<ESME[]> =>
  apiFetch<{ items: ESME[] }>(esmesPath).then((r) => r.items);

export const getESME = (systemID: string): Promise<ESME> =>
  apiFetch<ESME>(esmePath(systemID));

export const createESME = (esme: ESMECreate): Promise<CreatedESME> =>
  apiFetch<CreatedESME>(esmesPath, { method: "POST", body: esme });

export const updateESME = (systemID: string, esme: ESMEUpdate): Promise<ESME> =>
  apiFetch<ESME>(esmePath(systemID), { method: "PUT", body: esme });

export const deleteESME = (systemID: string): Promise<void> =>
  apiFetch<void>(esmePath(systemID), { method: "DELETE" });
