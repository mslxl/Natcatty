import React from "react";
import { cn } from "../../../../lib/utils";
import type { SettingsIconId } from "./types";
import { SETTINGS_ICON_PATHS, SETTINGS_ICON_COLORS } from "./types";

export const ProviderIconBadge: React.FC<{
  providerId: SettingsIconId;
  size?: "sm" | "md";
}> = ({ providerId, size = "md" }) => (
  <div
    className={cn(
      "rounded-md flex items-center justify-center shrink-0 overflow-hidden",
      size === "sm" ? "w-5 h-5" : "w-8 h-8",
      SETTINGS_ICON_COLORS[providerId],
    )}
  >
    <img
      src={SETTINGS_ICON_PATHS[providerId]}
      alt=""
      aria-hidden="true"
      draggable={false}
      className={cn(
        "object-contain brightness-0 invert",
        size === "sm" ? "w-3 h-3" : "w-4 h-4",
      )}
    />
  </div>
);
