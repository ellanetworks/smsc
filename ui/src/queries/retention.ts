import { apiFetch } from "@/queries/utils";

export interface RetentionSettings {
  days: number;
}

export const getRetention = (): Promise<RetentionSettings> =>
  apiFetch<RetentionSettings>("/api/v1/messages/retention");

export const updateRetention = (
  retention: RetentionSettings,
): Promise<RetentionSettings> =>
  apiFetch<RetentionSettings>("/api/v1/messages/retention", {
    method: "PUT",
    body: retention,
  });
