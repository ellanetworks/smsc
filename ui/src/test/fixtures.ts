import type { ClusterMember, ClusterStatus } from "@/queries/cluster";
import type { Attempt, Message } from "@/queries/messages";

export const message = (overrides: Partial<Message> = {}): Message => ({
  id: 1,
  from: "+15551230001",
  to: "+15551230002",
  encoding: "gsm7",
  status: "delivered",
  created_at: "2026-09-30T12:00:00.000Z",
  updated_at: "2026-09-30T12:00:01.000Z",
  expires_at: "2026-10-07T12:00:00.000Z",
  status_report: { requested: false, reports: [] },
  ...overrides,
});

export const attempt = (overrides: Partial<Attempt> = {}): Attempt => ({
  id: 1,
  started_at: "2026-09-30T12:00:00.000Z",
  completed_at: "2026-09-30T12:00:00.250Z",
  step: "routing",
  outcome: "success",
  result_code: 2001,
  ...overrides,
});

export const page = <T>(
  items: T[],
  extra: { page?: number; per_page?: number; total_count?: number } = {},
) => ({
  items,
  page: extra.page ?? 1,
  per_page: extra.per_page ?? 25,
  total_count: extra.total_count ?? items.length,
});

export const clusterMember = (
  overrides: Partial<ClusterMember> = {},
): ClusterMember => ({
  node_id: "0b5c6a0e-1111-4c1e-9a55-1f0c2d3e4f50",
  address: "10.0.0.11:7000",
  diameter_label: "smsc-1f0c2d3e4f50",
  diameter_host: "smsc-1f0c2d3e4f50.node.epc.mnc001.mcc001.3gppnetwork.org",
  label_generated: true,
  display_name: "",
  version: "v0.1.0",
  joined_at: "2026-09-30T12:00:00.000Z",
  drain_state: "active",
  suffrage: "voter",
  leader: false,
  healthy: true,
  last_contact: "12ms",
  ...overrides,
});

export const clusterStatus = (
  overrides: Partial<ClusterStatus> = {},
): ClusterStatus => ({
  node_id: "0b5c6a0e-1111-4c1e-9a55-1f0c2d3e4f50",
  cluster_id: "9f1e2d3c-2222-4b5a-8c7d-6e5f4a3b2c1d",
  clustered: true,
  leader_id: "0b5c6a0e-1111-4c1e-9a55-1f0c2d3e4f50",
  term: 3,
  applied_index: 42,
  commit_index: 42,
  schema: 1,
  healthy: true,
  failure_tolerance: 1,
  members: [
    clusterMember({ leader: true, last_contact: "0s" }),
    clusterMember({
      node_id: "1c6d7b1f-3333-4d2f-8b66-2a1d3e4f5a61",
      address: "10.0.0.12:7000",
      diameter_label: "smsc-b",
      diameter_host: "smsc-b.node.epc.mnc001.mcc001.3gppnetwork.org",
      label_generated: false,
      display_name: "paris-2",
      last_contact: "35ms",
    }),
    clusterMember({
      node_id: "2d7e8c2a-4444-4e3a-9c77-3b2e4f5a6b72",
      address: "10.0.0.13:7000",
      diameter_label: "smsc-3b2e4f5a6b72",
      diameter_host: "smsc-3b2e4f5a6b72.node.epc.mnc001.mcc001.3gppnetwork.org",
      label_generated: true,
      suffrage: "nonvoter",
      drain_state: "drained",
      healthy: false,
      last_contact: "9.5s",
    }),
  ],
  ...overrides,
});
