import { apiFetch } from "@/queries/utils";

// RetentionSettings is how long messages are kept once they have their final status.
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
