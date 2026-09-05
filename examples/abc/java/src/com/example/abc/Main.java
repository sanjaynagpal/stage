package com.example.abc;

import java.io.IOException;
import java.io.PrintWriter;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.Paths;
import java.nio.file.StandardOpenOption;
import java.time.Instant;
import java.util.Arrays;

import javax.swing.JOptionPane;

/**
 * Fixture Java app for manually exercising Stage/Rigger end to end
 * (docs/REQUIREMENTS.md §5-6). It reports every value Rigger is supposed to
 * have resolved and injected: program arguments and the system properties
 * set via jvmOptions in examples/abc/fakeserver/public/abc/manifest.json.
 *
 * Rigger launches via javaw.exe (no console attached), so this writes its
 * report to a log file as well as showing it in a dialog, unless
 * -Dabc.headless=true is set (used for automated/manual testing without a
 * display), in which case it prints to stdout instead.
 */
public final class Main {
    public static void main(String[] args) {
        String report = buildReport(args);
        writeLog(args, report);

        if (Boolean.getBoolean("abc.headless")) {
            System.out.println(report);
        } else {
            JOptionPane.showMessageDialog(null, report, "ABC (fixture)", JOptionPane.INFORMATION_MESSAGE);
        }
    }

    private static String buildReport(String[] args) {
        StringBuilder sb = new StringBuilder();
        sb.append("ABC fixture app launched at ").append(Instant.now()).append("\n\n");

        sb.append("Arguments:\n");
        if (args.length == 0) {
            sb.append("  (none)\n");
        }
        sb.append("  ").append(Arrays.toString(args)).append("\n");

        sb.append("\nSystem properties set by Rigger's jvmOptions:\n");
        for (String key : new String[] {"abc.env", "abc.token", "abc.zone", "abc.proxyHost", "abc.proxyPort"}) {
            sb.append("  ").append(key).append(" = ").append(System.getProperty(key, "(not set)")).append("\n");
        }
        return sb.toString();
    }

    /**
     * Writes the report to abc-launch.log under --data-dir (the manifest
     * passes ${dataDir} for this), falling back to the user's home
     * directory if that argument is missing.
     */
    private static void writeLog(String[] args, String report) {
        String dataDir = argValue(args, "--data-dir");
        Path dir = dataDir != null ? Paths.get(dataDir) : Paths.get(System.getProperty("user.home"));
        try {
            Files.createDirectories(dir);
            Path logPath = dir.resolve("abc-launch.log");
            try (PrintWriter out = new PrintWriter(Files.newBufferedWriter(logPath,
                    StandardOpenOption.CREATE, StandardOpenOption.APPEND))) {
                out.println(report);
            }
        } catch (IOException e) {
            // Best-effort only: a missing/unwritable log location shouldn't
            // stop the fixture app from reporting via the dialog/stdout.
        }
    }

    private static String argValue(String[] args, String name) {
        for (int i = 0; i < args.length - 1; i++) {
            if (name.equals(args[i])) {
                return args[i + 1];
            }
        }
        return null;
    }
}
