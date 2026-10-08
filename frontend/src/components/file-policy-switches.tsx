import { AudioLines, FileText, FileType2, Image } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Switch } from "@/components/ui/switch";
import type { FilePolicy } from "@/lib/types";
import { cn } from "@/lib/utils";

const GROUPS: Array<{ key: keyof FilePolicy; icon: typeof FileText; examples: string }> = [
  { key: "plainText", icon: FileText, examples: ".txt .md .csv .json .html" },
  { key: "documents", icon: FileType2, examples: ".pdf .docx .pptx .xlsx .odt" },
  { key: "images", icon: Image, examples: ".png .jpg .webp .gif .heic" },
  { key: "audios", icon: AudioLines, examples: ".mp3 .wav .m4a .ogg .flac" },
];

export const DEFAULT_FILE_POLICY: FilePolicy = { plainText: true, documents: true, images: false, audios: false };

export function FilePolicySwitches({
  value,
  onChange,
  disabled,
  idPrefix = "fp",
  groups,
}: {
  value: FilePolicy;
  onChange: (value: FilePolicy) => void;
  disabled?: boolean;
  idPrefix?: string;
  groups?: Array<keyof FilePolicy>;
}) {
  const { t } = useTranslation();
  return (
    <div className={cn("grid gap-2 sm:grid-cols-2", disabled && "opacity-60")}>
      {GROUPS.filter((group) => !groups || groups.includes(group.key)).map(({ key, icon: Icon, examples }) => (
        <label
          key={key}
          htmlFor={`${idPrefix}-${key}`}
          className="flex cursor-pointer items-center gap-3 rounded-md border px-3 py-2.5 transition-colors hover:bg-accent/40"
        >
          <span className="flex size-8 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground">
            <Icon className="size-4" />
          </span>
          <span className="min-w-0 flex-1">
            <span className="block text-sm font-medium">{t(`filePolicy.${key}`)}</span>
            <span className="block truncate font-mono text-[11px] text-muted-foreground">{groups && key === "images" ? ".png .jpg .webp .gif" : examples}</span>
          </span>
          <Switch
            id={`${idPrefix}-${key}`}
            checked={!!value[key]}
            disabled={disabled}
            onCheckedChange={(c) => onChange({ ...value, [key]: c })}
          />
        </label>
      ))}
    </div>
  );
}
