import { apiFetch } from "@/queries/utils";

export interface OperatorSettings {
  mcc: string;
  mnc: string;
  service_centre_address: string;
  numbering: {
    country_code: string;
    national_prefix: string;
    international_prefix: string;
  };
}

export const getOperator = (): Promise<OperatorSettings> =>
  apiFetch<OperatorSettings>("/api/v1/operator");

export const updateOperator = (
  operator: OperatorSettings,
): Promise<OperatorSettings> =>
  apiFetch<OperatorSettings>("/api/v1/operator", {
    method: "PUT",
    body: operator,
  });
