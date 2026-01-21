import {
  Database,
  ExternalLink,
  File,
  FileArchive,
  FileAudio,
  FileCode,
  FileImage,
  FileSpreadsheet,
  FileText,
  FileType,
  FileVideo,
  Folder,
  Globe,
  Lock,
  Settings,
  Terminal,
} from "lucide-react";
import React from "react";

export const getFileIcon = (fileName: string, isDirectory: boolean, isSymlink?: boolean) => {
  if (isDirectory)
    return (
      <Folder
        size={18}
        fill="currentColor"
        fillOpacity={0.2}
        className="text-blue-400"
      />
    );

  if (isSymlink) {
    return <ExternalLink size={18} className="text-cyan-500" />;
  }

  const ext = fileName.split(".").pop()?.toLowerCase() || "";

  if (["doc", "docx", "rtf", "odt"].includes(ext))
    return <FileText size={18} className="text-blue-500" />;
  if (["xls", "xlsx", "csv", "ods"].includes(ext))
    return <FileSpreadsheet size={18} className="text-green-500" />;
  if (["ppt", "pptx", "odp"].includes(ext))
    return <FileType size={18} className="text-orange-500" />;
  if (["pdf"].includes(ext))
    return <FileText size={18} className="text-red-500" />;

  if (["js", "jsx", "ts", "tsx", "mjs", "cjs"].includes(ext))
    return <FileCode size={18} className="text-yellow-500" />;
  if (["py", "pyc", "pyw"].includes(ext))
    return <FileCode size={18} className="text-blue-400" />;
  if (["sh", "bash", "zsh", "fish", "bat", "cmd", "ps1"].includes(ext))
    return <Terminal size={18} className="text-green-400" />;
  if (["c", "cpp", "h", "hpp", "cc", "cxx"].includes(ext))
    return <FileCode size={18} className="text-blue-600" />;
  if (["java", "class", "jar"].includes(ext))
    return <FileCode size={18} className="text-orange-600" />;
  if (["go"].includes(ext))
    return <FileCode size={18} className="text-cyan-500" />;
  if (["rs"].includes(ext))
    return <FileCode size={18} className="text-orange-400" />;
  if (["rb"].includes(ext))
    return <FileCode size={18} className="text-red-400" />;
  if (["php"].includes(ext))
    return <FileCode size={18} className="text-purple-500" />;
  if (["html", "htm", "xhtml"].includes(ext))
    return <Globe size={18} className="text-orange-500" />;
  if (["css", "scss", "sass", "less"].includes(ext))
    return <FileCode size={18} className="text-blue-500" />;
  if (["vue", "svelte"].includes(ext))
    return <FileCode size={18} className="text-green-500" />;

  if (["json", "json5"].includes(ext))
    return <FileCode size={18} className="text-yellow-600" />;
  if (["xml", "xsl", "xslt"].includes(ext))
    return <FileCode size={18} className="text-orange-400" />;
  if (["yml", "yaml"].includes(ext))
    return <Settings size={18} className="text-pink-400" />;
  if (["toml", "ini", "conf", "cfg", "config"].includes(ext))
    return <Settings size={18} className="text-gray-400" />;
  if (["env"].includes(ext))
    return <Lock size={18} className="text-yellow-500" />;
  if (["sql", "sqlite", "db"].includes(ext))
    return <Database size={18} className="text-blue-400" />;

  if (
    [
      "jpg",
      "jpeg",
      "png",
      "gif",
      "bmp",
      "webp",
      "svg",
      "ico",
      "tiff",
      "tif",
      "heic",
      "heif",
      "avif",
    ].includes(ext)
  )
    return <FileImage size={18} className="text-purple-400" />;

  if (
    [
      "mp4",
      "mkv",
      "avi",
      "mov",
      "wmv",
      "flv",
      "webm",
      "m4v",
      "3gp",
      "mpeg",
      "mpg",
    ].includes(ext)
  )
    return <FileVideo size={18} className="text-pink-500" />;

  if (
    ["mp3", "wav", "flac", "aac", "ogg", "m4a", "wma", "opus", "aiff"].includes(
      ext,
    )
  )
    return <FileAudio size={18} className="text-green-400" />;

  if (
    [
      "zip",
      "rar",
      "7z",
      "tar",
      "gz",
      "bz2",
      "xz",
      "tgz",
      "tbz2",
      "lz",
      "lzma",
      "cab",
      "iso",
      "dmg",
    ].includes(ext)
  )
    return <FileArchive size={18} className="text-yellow-600" />;

  return <File size={18} className="text-muted-foreground" />;
};
