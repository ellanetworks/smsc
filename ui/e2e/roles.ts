import { RoleID } from "./api";

export const ROLE_PASSWORD = "E2eRolePassw0rd!";

// ROLES are the users of the roles other than Admin, and the session file of each.
export const ROLES = {
  "Read Only": {
    email: "e2e-readonly@ellanetworks.com",
    roleId: RoleID.ReadOnly,
    session: "e2e/.auth/readonly.json",
  },
  "Network Manager": {
    email: "e2e-netmanager@ellanetworks.com",
    roleId: RoleID.NetworkManager,
    session: "e2e/.auth/network-manager.json",
  },
  Submitter: {
    email: "e2e-submitter@ellanetworks.com",
    roleId: RoleID.Submitter,
    session: "e2e/.auth/submitter.json",
  },
} as const;
