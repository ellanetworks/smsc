import { useState } from "react";
import { Box } from "@mui/material";
import { useQuery } from "@tanstack/react-query";
import EditNumberingDialog from "@/components/EditNumberingDialog";
import EditOperatorIdDialog from "@/components/EditOperatorIdDialog";
import EditServiceCentreDialog from "@/components/EditServiceCentreDialog";
import PageHeader from "@/components/PageHeader";
import QueryAlert from "@/components/QueryAlert";
import SettingsTable, { SubFields } from "@/components/SettingsTable";
import { getOperator } from "@/queries/operator";

const orNA = (value: string) => value || "N/A";

export default function Operator() {
  const [editing, setEditing] = useState<"id" | "address" | "numbering" | null>(
    null,
  );
  const { data, error, isPending } = useQuery({
    queryKey: ["operator"],
    queryFn: getOperator,
  });

  return (
    <Box component="section" aria-labelledby="operator-title">
      <PageHeader
        id="operator-title"
        title="Operator"
        description="Your network's identity, SMSC number and numbering plan."
      />
      <QueryAlert
        error={error}
        hasData={data !== undefined}
        subject="operator settings"
      />
      <SettingsTable
        label="Operator settings"
        loading={isPending}
        rows={[
          {
            label: "Operator ID (MCC/MNC)",
            help: "Mobile Country Code and Mobile Network Code of your network. They set this SMSC's Diameter host and realm.",
            value: data && `${data.mcc} / ${data.mnc}`,
            onEdit: () => setEditing("id"),
          },
          {
            label: "Service Centre Address",
            help: "The SMSC number, in international format. Phones need the same number on their SIM cards.",
            value: data?.service_centre_address,
            onEdit: () => setEditing("address"),
          },
          {
            label: "Numbering",
            help: "Used to turn the national numbers phones send into international ones.",
            value: data && (
              <SubFields
                rows={[
                  ["Country Code", data.numbering.country_code],
                  ["National Prefix", orNA(data.numbering.national_prefix)],
                  [
                    "International Prefix",
                    orNA(data.numbering.international_prefix),
                  ],
                ]}
              />
            ),
            onEdit: () => setEditing("numbering"),
          },
        ]}
      />
      {editing === "id" && data && (
        <EditOperatorIdDialog
          operator={data}
          onClose={() => setEditing(null)}
        />
      )}
      {editing === "address" && data && (
        <EditServiceCentreDialog
          operator={data}
          onClose={() => setEditing(null)}
        />
      )}
      {editing === "numbering" && data && (
        <EditNumberingDialog operator={data} onClose={() => setEditing(null)} />
      )}
    </Box>
  );
}
