import { apiFetch } from "@/queries/utils";

export interface Status {
  version: string;
  revision: string;
  initialized: boolean;
  clustered: boolean;
  node_id: string;
  display_name: string;
  diameter_label: string;
}

export const getStatus = (): Promise<Status> =>
  apiFetch<Status>("/api/v1/status");
