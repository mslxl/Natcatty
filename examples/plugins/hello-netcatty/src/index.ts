import { definePlugin } from "@netcatty/plugin-sdk";

export default definePlugin({
  activate(context) {
    context.logger.info("Hello Netcatty example activated", {
      pluginId: context.pluginId,
    });
    context.subscriptions.add(context.commands.registerCommand(
      "com.netcatty.hello.sayHello",
      async () => {
        const greeting = await context.settings.get<string>("com.netcatty.hello.greeting");
        context.logger.info(greeting ?? "Hello from Netcatty");
        return { greeting: greeting ?? "Hello from Netcatty" };
      },
    ));
  },
});
