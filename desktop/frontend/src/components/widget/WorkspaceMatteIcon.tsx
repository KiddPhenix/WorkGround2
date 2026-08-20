import browser from "../../assets/workspace-icons-matte-v1/browser.png";
import build from "../../assets/workspace-icons-matte-v1/build.png";
import cmd from "../../assets/workspace-icons-matte-v1/cmd.png";
import cpp from "../../assets/workspace-icons-matte-v1/cpp.png";
import csharp from "../../assets/workspace-icons-matte-v1/csharp.png";
import dart from "../../assets/workspace-icons-matte-v1/dart.png";
import data from "../../assets/workspace-icons-matte-v1/data.png";
import database from "../../assets/workspace-icons-matte-v1/database.png";
import delegate from "../../assets/workspace-icons-matte-v1/delegate-v2.png";
import design from "../../assets/workspace-icons-matte-v1/design.png";
import discussion from "../../assets/workspace-icons-matte-v1/discussion.png";
import document from "../../assets/workspace-icons-matte-v1/document.png";
import edit from "../../assets/workspace-icons-matte-v1/edit.png";
import folder from "../../assets/workspace-icons-matte-v1/folder.png";
import game from "../../assets/workspace-icons-matte-v1/game.png";
import go from "../../assets/workspace-icons-matte-v1/go.png";
import java from "../../assets/workspace-icons-matte-v1/java.png";
import javascript from "../../assets/workspace-icons-matte-v1/javascript.png";
import music from "../../assets/workspace-icons-matte-v1/music.png";
import newIcon from "../../assets/workspace-icons-matte-v1/new-v2.png";
import php from "../../assets/workspace-icons-matte-v1/php.png";
import presentation from "../../assets/workspace-icons-matte-v1/presentation.png";
import publish from "../../assets/workspace-icons-matte-v1/publish.png";
import python from "../../assets/workspace-icons-matte-v1/python.png";
import react from "../../assets/workspace-icons-matte-v1/react.png";
import research from "../../assets/workspace-icons-matte-v1/research.png";
import run from "../../assets/workspace-icons-matte-v1/run.png";
import rust from "../../assets/workspace-icons-matte-v1/rust.png";
import sport from "../../assets/workspace-icons-matte-v1/sport.png";
import sync from "../../assets/workspace-icons-matte-v1/sync.png";
import test from "../../assets/workspace-icons-matte-v1/test.png";
import typescript from "../../assets/workspace-icons-matte-v1/typescript.png";
import unity from "../../assets/workspace-icons-matte-v1/unity.png";
import video from "../../assets/workspace-icons-matte-v1/video.png";
import type { WorkspaceMatteIconKey } from "../../lib/projectIcons";

const iconAssets: Record<WorkspaceMatteIconKey, string> = {
  browser, build, cmd, cpp, csharp, dart, data, database, delegate, design, discussion,
  document, edit, folder, game, go, java, javascript, music, new: newIcon, php, presentation,
  publish, python, react, research, run, rust, sport, sync, test, typescript,
  unity, video,
};

export function WorkspaceMatteIcon({ icon, className }: { icon: WorkspaceMatteIconKey; className?: string }) {
  return <img src={iconAssets[icon]} className={className} alt="" aria-hidden="true" draggable={false} />;
}
