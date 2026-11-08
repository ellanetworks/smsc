import { Tooltip } from "@mui/material";
import DomainName from "@/components/DomainName";

const sameName = (a: string, b: string) => a.toLowerCase() === b.toLowerCase();

const inRealm = (host: string, realm: string) =>
  realm !== "" && host.toLowerCase().endsWith(`.${realm.toLowerCase()}`);

function Abbreviated({ full, label }: { full: string; label: string }) {
  return (
    <Tooltip title={full} arrow>
      <span>{label}</span>
    </Tooltip>
  );
}

export function HostInRealm({ host, realm }: { host: string; realm: string }) {
  if (!inRealm(host, realm)) {
    return <DomainName name={host} />;
  }
  return <Abbreviated full={host} label={host.slice(0, -(realm.length + 1))} />;
}

export function RealmName({ name, realm }: { name: string; realm: string }) {
  if (realm === "" || !sameName(name, realm)) {
    return <DomainName name={name} />;
  }
  return <Abbreviated full={name} label="Home realm" />;
}
