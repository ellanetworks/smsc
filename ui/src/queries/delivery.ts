import { apiFetch } from "@/queries/utils";

export interface DeliverySettings {
  default_validity_seconds: number;
  retry_intervals_seconds: number[];
}

export const getDelivery = (): Promise<DeliverySettings> =>
  apiFetch<DeliverySettings>("/api/v1/delivery");

export const updateDelivery = (
  delivery: DeliverySettings,
): Promise<DeliverySettings> =>
  apiFetch<DeliverySettings>("/api/v1/delivery", {
    method: "PUT",
    body: delivery,
  });
